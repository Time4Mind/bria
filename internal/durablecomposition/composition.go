// Package durablecomposition wires controller turn custody to the durable
// journal. It owns no provider process or Telegram transport implementation.
package durablecomposition

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/sessionsupervisor"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramnotify"
)

const inputSweepInterval = time.Second

type InputProcessor interface {
	ProcessDurableInput(context.Context, telegramcontroller.DurableLeasedInput, telegramcontroller.DurableInputCallbacks) (telegramcontroller.DurableInputProcessReceipt, error)
}

type ControllerInputProcessor struct{ processor InputProcessor }

func NewControllerInputProcessor(processor InputProcessor) *ControllerInputProcessor {
	return &ControllerInputProcessor{processor: processor}
}

func (processor *ControllerInputProcessor) Process(ctx context.Context, input durableflow.ProviderInput, callbacks durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
	result := durableflow.InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.InputProcessUnknown}
	if processor == nil || processor.processor == nil || callbacks.OnAccepted == nil {
		return result, errors.New("durable controller input processor is required")
	}
	receipt, processErr := processor.processor.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{
		SessionID: domain.SessionID(input.SessionID), MessageID: input.MessageID, Sequence: input.Sequence,
		Payload: append([]byte(nil), input.Payload...), Attachments: attachmentsFromJournal(input.Attachments),
	}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(callbackCtx context.Context, acceptance telegramcontroller.DurableInputAcceptance) error {
		if acceptance.SessionID != domain.SessionID(input.SessionID) || acceptance.MessageID != input.MessageID || acceptance.Sequence != input.Sequence {
			return durableflow.ErrInvalidHandoff
		}
		return callbacks.OnAccepted(callbackCtx, durableflow.HandoffResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: durableflow.HandoffAccepted})
	}})
	if processErr != nil {
		return result, processErr
	}
	if receipt.SessionID != domain.SessionID(input.SessionID) || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence || !receipt.Accepted {
		return result, errors.New("durable processor returned a mismatched receipt")
	}
	switch receipt.Completion {
	case telegramcontroller.DurableInputSucceeded:
		result.State = durableflow.InputProcessCompleted
	case telegramcontroller.DurableInputFailed:
		result.State = durableflow.InputProcessFailed
	default:
		return result, errors.New("durable processor returned an invalid completion")
	}
	return result, nil
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
	if custody.Wake != nil {
		select {
		case custody.Wake <- input.SessionID:
		default:
		}
	}
	return result, nil
}

func attachmentsToJournal(attachments []telegramcontroller.AttachmentRef) []messagejournal.AttachmentRef {
	result := make([]messagejournal.AttachmentRef, len(attachments))
	for index, attachment := range attachments {
		result[index] = messagejournal.AttachmentRef{Reference: attachment.Reference, Size: attachment.Size, SHA256: attachment.SHA256}
	}
	return result
}

func attachmentsFromJournal(attachments []messagejournal.AttachmentRef) []telegramcontroller.AttachmentRef {
	result := make([]telegramcontroller.AttachmentRef, len(attachments))
	for index, attachment := range attachments {
		result[index] = telegramcontroller.AttachmentRef{Reference: attachment.Reference, Size: attachment.Size, SHA256: attachment.SHA256}
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
	ticker := time.NewTicker(inputSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			workers.Wait()
			return ctx.Err()
		case id := <-dispatcher.Wake:
			wakeSession(id)
		case <-ticker.C:
			wakeAll()
		}
	}
}

func (dispatcher InputDispatcher) ProcessReadySession(ctx context.Context, id domain.SessionID) error {
	session, err := dispatcher.Sessions.Load(ctx, id)
	if err != nil {
		return err
	}
	if session.Status() != domain.SessionReady {
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
		if result.State != durableflow.InputProcessCompleted {
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
	case telegramcontroller.NotificationCommentary, telegramcontroller.NotificationQuestion, telegramcontroller.NotificationFinal, telegramcontroller.NotificationError:
	default:
		return result, errors.New("durable Telegram notification kind is unsupported")
	}
	receipt, deliverErr := sender.Deliverer.Deliver(ctx, telegramcontroller.Notification{OperationID: output.OperationID, ConversationID: sender.OwnerPrivateChatID, SessionID: domain.SessionID(output.SessionID), Kind: kind, Text: string(output.Payload)}, output.OperationID)
	if receipt.OperationID != output.OperationID {
		return result, errors.Join(deliverErr, errors.New("Telegram delivery receipt identity mismatch"))
	}
	switch receipt.State {
	case telegramnotify.DeliveryConfirmed:
		if deliverErr != nil || len(receipt.Parts) == 0 {
			return result, errors.Join(deliverErr, errors.New("confirmed Telegram delivery has no complete receipt"))
		}
		for _, part := range receipt.Parts {
			if part.PartID == "" || part.MessageID <= 0 {
				return result, errors.New("confirmed Telegram delivery has an invalid part receipt")
			}
		}
		result.State, result.Receipt = durableflow.DeliveryConfirmed, "telegram:"+output.OperationID+":confirmed"
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
	if custody.Wake != nil {
		select {
		case custody.Wake <- output.SessionID:
		default:
		}
	}
	return result, nil
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
	ticker := time.NewTicker(inputSweepInterval)
	defer ticker.Stop()
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
		case <-ticker.C:
			deliverAll()
		}
	}
}

var errAcceptedTurnHistoryUnverifiable = errors.New("accepted turn provider history is unverifiable")

type AcceptedTurnReconciler struct {
	Flow      *durableflow.Flow
	Histories map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler
}

func (reconciler AcceptedTurnReconciler) ReconcileAcceptedTurns(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) (sessionsupervisor.AcceptedTurnReconciliation, error) {
	if reconciler.Flow == nil || strings.TrimSpace(string(sessionID)) == "" {
		return sessionsupervisor.AcceptedTurnReconciliation{}, errAcceptedTurnHistoryUnverifiable
	}
	resolver := &acceptedInputHistoryResolver{history: reconciler.Histories[binding.Provider], sessionID: sessionID, binding: binding}
	results, err := reconciler.Flow.ReconcileAcceptedInputs(ctx, string(sessionID), resolver)
	receipt := sessionsupervisor.AcceptedTurnReconciliation{Turns: make([]sessionsupervisor.ReconciledAcceptedTurn, 0, len(results))}
	for _, result := range results {
		outcome := sessionsupervisor.AcceptedTurnUnknown
		switch result.Resolution {
		case durableflow.AcceptedCompleted:
			outcome = sessionsupervisor.AcceptedTurnCompleted
		case durableflow.AcceptedFailed:
			outcome = sessionsupervisor.AcceptedTurnFailed
		case durableflow.AcceptedUnknown:
		default:
			err = errors.Join(err, errAcceptedTurnHistoryUnverifiable)
		}
		receipt.Turns = append(receipt.Turns, sessionsupervisor.ReconciledAcceptedTurn{MessageID: result.MessageID, Outcome: outcome})
	}
	return receipt, err
}

type acceptedInputHistoryResolver struct {
	history   sessionsupervisor.AcceptedTurnReconciler
	sessionID domain.SessionID
	binding   domain.ProviderBinding
	loaded    bool
	outcomes  map[string]sessionsupervisor.AcceptedTurnOutcome
	err       error
}

func (resolver *acceptedInputHistoryResolver) ResolveAccepted(ctx context.Context, input durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
	result := durableflow.AcceptedResolutionResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Resolution: durableflow.AcceptedUnknown}
	if resolver == nil || input.SessionID != string(resolver.sessionID) || strings.TrimSpace(input.MessageID) == "" {
		return result, errAcceptedTurnHistoryUnverifiable
	}
	resolver.load(ctx)
	if resolver.err != nil {
		return result, resolver.err
	}
	outcome, found := resolver.outcomes[input.MessageID]
	if !found {
		return result, fmt.Errorf("%w: message %q is absent", errAcceptedTurnHistoryUnverifiable, input.MessageID)
	}
	switch outcome {
	case sessionsupervisor.AcceptedTurnCompleted:
		result.Resolution = durableflow.AcceptedCompleted
	case sessionsupervisor.AcceptedTurnFailed:
		result.Resolution = durableflow.AcceptedFailed
	case sessionsupervisor.AcceptedTurnUnknown:
	default:
		return result, errAcceptedTurnHistoryUnverifiable
	}
	return result, nil
}

func (resolver *acceptedInputHistoryResolver) load(ctx context.Context) {
	if resolver.loaded {
		return
	}
	resolver.loaded = true
	if resolver.history == nil {
		resolver.err = errAcceptedTurnHistoryUnverifiable
		return
	}
	receipt, err := resolver.history.ReconcileAcceptedTurns(ctx, resolver.sessionID, resolver.binding)
	if err != nil {
		resolver.err = err
		return
	}
	resolver.outcomes = make(map[string]sessionsupervisor.AcceptedTurnOutcome, len(receipt.Turns))
	for _, turn := range receipt.Turns {
		if strings.TrimSpace(turn.MessageID) == "" || strings.TrimSpace(turn.MessageID) != turn.MessageID {
			resolver.err = errAcceptedTurnHistoryUnverifiable
			return
		}
		if _, duplicate := resolver.outcomes[turn.MessageID]; duplicate {
			resolver.err = errAcceptedTurnHistoryUnverifiable
			return
		}
		switch turn.Outcome {
		case sessionsupervisor.AcceptedTurnCompleted, sessionsupervisor.AcceptedTurnFailed, sessionsupervisor.AcceptedTurnUnknown:
			resolver.outcomes[turn.MessageID] = turn.Outcome
		default:
			resolver.err = errAcceptedTurnHistoryUnverifiable
			return
		}
	}
}

var _ durableflow.InputProcessor = (*ControllerInputProcessor)(nil)
var _ telegramcontroller.DurableInputCustody = InputCustody{}
var _ telegramcontroller.DurableOutputCustody = OutputCustody{}
var _ sessionsupervisor.AcceptedTurnReconciler = AcceptedTurnReconciler{}
var _ durableflow.AcceptedInputResolver = (*acceptedInputHistoryResolver)(nil)
