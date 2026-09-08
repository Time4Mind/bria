// Package durablecomposition wires controller turn custody to the durable
// journal. It owns no provider process or Telegram transport implementation.
package durablecomposition

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/durableinputbridge"
	"bria/internal/messagejournal"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

type InputProcessor = durableinputbridge.InputProcessor
type ControllerInputProcessor = durableinputbridge.Processor

func NewControllerInputProcessor(processor InputProcessor, notify ...func(domain.SessionID)) *ControllerInputProcessor {
	return durableinputbridge.New(processor, notify...)
}

type InputCustody struct {
	Flow *durableflow.Flow
	Wake chan domain.SessionID
}

func (custody InputCustody) Accept(ctx context.Context, input telegramcontroller.SessionInput) (telegramcontroller.InputReceipt, error) {
	if custody.Flow == nil {
		return telegramcontroller.InputReceipt{}, errors.New("durable input flow is required")
	}
	receipt, err := custody.Flow.EnqueueInputWithAttachments(ctx, string(input.SessionID), input.MessageID, input.Payload, attachmentsToJournal(input.Attachments))
	result := telegramcontroller.InputReceipt{Inserted: receipt.Inserted, SessionID: domain.SessionID(receipt.SessionID), MessageID: receipt.MessageID, Sequence: receipt.Sequence}
	if err != nil {
		return result, err
	}
	if result.SessionID != input.SessionID || result.MessageID != input.MessageID || result.Sequence == 0 {
		return telegramcontroller.InputReceipt{}, durableflow.ErrInvalidHandoff
	}
	custody.WakeSession(input.SessionID)
	return result, nil
}

// WakeSession coalesces a bounded, nonblocking dispatch hint for this session.
func (custody InputCustody) WakeSession(id domain.SessionID) {
	if id == "" {
		return
	}
	select {
	case custody.Wake <- id:
	default:
	}
}

func attachmentsToJournal(attachments []telegramcontroller.AttachmentRef) []messagejournal.AttachmentRef {
	result := make([]messagejournal.AttachmentRef, len(attachments))
	for index, attachment := range attachments {
		result[index] = messagejournal.AttachmentRef{Reference: attachment.Reference, Size: attachment.Size, SHA256: attachment.SHA256}
	}
	return result
}

type SessionStore interface {
	List(context.Context) ([]domain.Session, error)
	Load(context.Context, domain.SessionID) (domain.Session, error)
}

type InputDispatcher struct {
	Flow      *durableflow.Flow
	Processor durableflow.InputProcessor
	Sessions  SessionStore
	Wake      <-chan domain.SessionID
	Report    func(error)
}

func (dispatcher InputDispatcher) Run(ctx context.Context) error {
	if ctx == nil || dispatcher.Flow == nil || dispatcher.Processor == nil || dispatcher.Sessions == nil || dispatcher.Wake == nil || dispatcher.Report == nil {
		return errors.New("durable input dispatcher dependencies are required")
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	channels := make(map[domain.SessionID]chan struct{})
	wakeSession := func(id domain.SessionID) {
		if id == "" {
			return
		}
		wake := channels[id]
		if wake == nil {
			wake = make(chan struct{}, 1)
			channels[id] = wake
			workers.Add(1)
			go func(sessionID domain.SessionID, wake <-chan struct{}) {
				defer workers.Done()
				for {
					select {
					case <-workerContext.Done():
						return
					case <-wake:
						if err := dispatcher.ProcessReadySession(workerContext, sessionID); err != nil && !errors.Is(err, context.Canceled) {
							dispatcher.Report(err)
						}
					}
				}
			}(id, wake)
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	wakeAll := func() {
		sessions, err := dispatcher.Sessions.List(workerContext)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				dispatcher.Report(err)
			}
			return
		}
		for _, session := range sessions {
			wakeSession(session.ID())
		}
	}
	wakeAll()
	for {
		select {
		case <-ctx.Done():
			cancel()
			workers.Wait()
			return ctx.Err()
		case id := <-dispatcher.Wake:
			wakeSession(id)
		}
	}
}

func (dispatcher InputDispatcher) ProcessReadySession(ctx context.Context, id domain.SessionID) error {
	session, err := dispatcher.Sessions.Load(ctx, id)
	if err != nil {
		return err
	}
	if session.Status() != domain.SessionReady && session.Status() != domain.SessionRunning {
		return nil
	}
	for {
		result, err := dispatcher.Flow.ProcessNextInput(ctx, string(id), dispatcher.Processor)
		if errors.Is(err, messagejournal.ErrNoAvailable) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("process durable input for session %s: %w", id, err)
		}
		if result.State != durableflow.InputProcessCompleted && result.State != durableflow.InputProcessAccepted && result.State != durableflow.InputProcessTerminalFailed {
			return nil
		}
	}
}

type TelegramNotificationDeliverer interface {
	Deliver(context.Context, telegramcontroller.Notification, string) (telegramnotify.DeliveryReceipt, error)
}

type TelegramOutputSender struct {
	OwnerPrivateChatID int64
	Deliverer          TelegramNotificationDeliverer
}

func (sender TelegramOutputSender) Deliver(ctx context.Context, output durableflow.ProviderOutput) (durableflow.DeliveryResult, error) {
	result := durableflow.DeliveryResult{SessionID: output.SessionID, OperationID: output.OperationID, Sequence: output.Sequence, State: durableflow.DeliveryUnknown}
	if sender.OwnerPrivateChatID <= 0 || sender.Deliverer == nil || output.SessionID == "" || output.OperationID == "" || output.Sequence == 0 {
		return result, errors.New("durable Telegram delivery identity is required")
	}
	kind := telegramcontroller.NotificationKind(output.Kind)
	switch kind {
	case telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion, telegramcontroller.NotificationFinal, telegramcontroller.NotificationError, telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationNativeScreen:
	default:
		return result, errors.New("durable Telegram notification kind is unsupported")
	}
	receipt, deliverErr := sender.Deliverer.Deliver(ctx, telegramcontroller.Notification{OperationID: output.OperationID, ConversationID: sender.OwnerPrivateChatID, SessionID: domain.SessionID(output.SessionID), Kind: kind, Text: string(output.Payload)}, output.OperationID)
	if receipt.OperationID != output.OperationID {
		return result, errors.Join(deliverErr, errors.New("Telegram delivery receipt identity mismatch"))
	}
	switch receipt.State {
	case telegramnotify.DeliveryConfirmed:
		if deliverErr != nil || len(receipt.Parts) == 0 && !receipt.Suppressed || len(receipt.Parts) > 0 && receipt.Suppressed {
			return result, errors.Join(deliverErr, errors.New("confirmed Telegram delivery has no complete receipt"))
		}
		for _, part := range receipt.Parts {
			if part.PartID == "" || part.MessageID <= 0 {
				return result, errors.New("confirmed Telegram delivery has an invalid part receipt")
			}
		}
		receiptSuffix := "confirmed"
		if receipt.Suppressed {
			receiptSuffix = "suppressed"
		}
		result.State, result.Receipt = durableflow.DeliveryConfirmed, "telegram:"+output.OperationID+":"+receiptSuffix
		return result, nil
	case telegramnotify.DeliveryFailed:
		return durableflow.DeliveryResult{SessionID: output.SessionID, OperationID: output.OperationID, Sequence: output.Sequence, State: durableflow.DeliveryFailed}, nil
	case telegramnotify.DeliveryUnknown:
		return result, deliverErr
	default:
		return result, errors.Join(deliverErr, errors.New("Telegram delivery state is invalid"))
	}
}

type OutputCustody struct {
	Flow               *durableflow.Flow
	Wake               chan domain.SessionID
	OwnerPrivateChatID int64
}

func (custody OutputCustody) AcceptOutput(ctx context.Context, output telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
	if custody.Flow == nil || custody.OwnerPrivateChatID <= 0 || output.ConversationID != custody.OwnerPrivateChatID {
		return telegramcontroller.OutputReceipt{}, errors.New("durable output custody identity is invalid")
	}
	receipt, err := custody.Flow.EnqueueOutput(ctx, string(output.SessionID), output.OperationID, string(output.Kind), output.Payload)
	result := telegramcontroller.OutputReceipt{Inserted: receipt.Inserted, SessionID: domain.SessionID(receipt.SessionID), OperationID: receipt.OperationID, Sequence: receipt.Sequence}
	if err != nil {
		return result, err
	}
	if result.SessionID != output.SessionID || result.OperationID != output.OperationID || result.Sequence == 0 {
		return telegramcontroller.OutputReceipt{}, durableflow.ErrInvalidDelivery
	}
	if kinds := coalescedStateKinds(output.Kind); len(kinds) > 0 {
		if err := custody.Flow.SupersedePendingOutputs(ctx, string(output.SessionID), output.OperationID, kinds); err != nil {
			return result, err
		}
	}
	if custody.Wake != nil {
		select {
		case custody.Wake <- output.SessionID:
		default:
		}
	}
	return result, nil
}

func coalescedStateKinds(kind telegramcontroller.NotificationKind) []string {
	switch kind {
	case telegramcontroller.NotificationCommentary, telegramcontroller.NotificationPromptStatus, telegramcontroller.NotificationNativeScreen,
		telegramcontroller.NotificationFinal, telegramcontroller.NotificationError:
		return []string{string(telegramcontroller.NotificationCommentary), string(telegramcontroller.NotificationPromptStatus), string(telegramcontroller.NotificationNativeScreen)}
	default:
		return nil
	}
}

type OutputDispatcher struct {
	Flow     *durableflow.Flow
	Sessions SessionStore
	Wake     <-chan domain.SessionID
	Report   func(error)
}

func (dispatcher OutputDispatcher) Run(ctx context.Context) error {
	if ctx == nil || dispatcher.Flow == nil || dispatcher.Sessions == nil || dispatcher.Wake == nil || dispatcher.Report == nil {
		return errors.New("durable output dispatcher dependencies are required")
	}
	deliver := func(id domain.SessionID) {
		for {
			result, err := dispatcher.Flow.DeliverNextOutput(ctx, string(id))
			if errors.Is(err, messagejournal.ErrNoAvailable) {
				return
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					dispatcher.Report(fmt.Errorf("deliver durable output for session %s: %w", id, err))
				}
				return
			}
			if result.State != durableflow.DeliveryConfirmed {
				return
			}
		}
	}
	deliverAll := func() {
		sessions, err := dispatcher.Sessions.List(ctx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				dispatcher.Report(err)
			}
			return
		}
		for _, session := range sessions {
			deliver(session.ID())
		}
	}
	deliverAll()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case id := <-dispatcher.Wake:
			deliver(id)
		}
	}
}

var _ durableflow.InputProcessor = (*ControllerInputProcessor)(nil)
var _ telegramcontroller.DurableInputCustody = InputCustody{}
var _ telegramcontroller.DurableOutputCustody = OutputCustody{}
