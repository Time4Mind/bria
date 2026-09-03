package interactionflow

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

const (
	defaultInteractionTimeout = 15 * time.Minute
	maxSurfaceTextBytes       = 3500
)

var (
	ErrInvalidConfiguration    = errors.New("invalid provider interaction flow configuration")
	ErrInvalidEnvelope         = errors.New("invalid provider interaction envelope")
	ErrInvalidCallback         = errors.New("invalid provider interaction callback")
	ErrStaleCallback           = errors.New("stale provider interaction callback")
	ErrSendUnknown             = errors.New("provider interaction Telegram send is unknown")
	ErrProviderResponseUnknown = errors.New("provider interaction response handoff is unknown")
)

type Delivery struct {
	OperationID       string
	SessionID         domain.SessionID
	MessageID         string
	ProviderRequestID string
	ConversationID    int64
	Surface           telegramflow.SurfaceOutput
}

type DeliveryReceipt struct {
	OperationID      string
	CarrierMessageID int64
}

type ResponseAcceptance = telegramcontroller.InteractionResponseAcceptance

// DeliverySender owns one initial provider-originated Telegram mutation. Flow
// persists PhaseSendUnknown before invoking it, so any error is conservatively
// ambiguous and never automatically retried.
type DeliverySender interface {
	Deliver(context.Context, Delivery) (DeliveryReceipt, error)
}

type SecretMessageDeleter interface {
	DeleteMessage(context.Context, int64, int64) error
}

type Options struct {
	ConversationID int64
	OwnerActorID   int64
	SecretDeleter  SecretMessageDeleter
	Timeout        time.Duration
	Now            func() time.Time
}

type Flow struct {
	store          Store
	sender         DeliverySender
	conversationID int64
	timeout        time.Duration
	now            func() time.Time
	ownerActorID   int64
	secretDeleter  SecretMessageDeleter

	wakeMu          sync.Mutex
	wake            map[string]chan struct{}
	secretMu        sync.Mutex
	secretResponses map[string]sessionruntime.InteractionResponse
	secretInFlight  map[string]bool
}

func New(store Store, sender DeliverySender, options Options) (*Flow, error) {
	if store == nil || sender == nil || options.ConversationID <= 0 {
		return nil, ErrInvalidConfiguration
	}
	if options.Timeout == 0 {
		options.Timeout = defaultInteractionTimeout
	}
	if options.Timeout <= 0 || options.Timeout > 24*time.Hour {
		return nil, ErrInvalidConfiguration
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Flow{
		store: store, sender: sender, conversationID: options.ConversationID,
		ownerActorID: options.OwnerActorID, secretDeleter: options.SecretDeleter,
		timeout: options.Timeout, now: options.Now, wake: make(map[string]chan struct{}),
		secretResponses: make(map[string]sessionruntime.InteractionResponse), secretInFlight: make(map[string]bool),
	}, nil
}

// ResolveInteraction implements telegramcontroller.InteractionHandler. It
// blocks only after the exact request identity has durable custody and the
// initial Telegram carrier has a confirmed receipt.
func (flow *Flow) ResolveInteraction(ctx context.Context, envelope telegramcontroller.InteractionEnvelope) (sessionruntime.InteractionResponse, error) {
	if flow == nil || ctx == nil || !validEnvelope(envelope) {
		return sessionruntime.InteractionResponse{}, ErrInvalidEnvelope
	}
	operationID := operationID(envelope)
	operation, found, err := flow.store.Load(ctx, operationID)
	if err != nil {
		return sessionruntime.InteractionResponse{}, err
	}
	if !found {
		now := flow.now().UTC()
		operation, err = flow.store.Create(ctx, Operation{
			ID: operationID, SessionID: envelope.SessionID, MessageID: envelope.MessageID,
			ProviderRequestID: envelope.Request.ID, ConversationID: flow.conversationID,
			Request: envelope.Request, Phase: PhasePrepared, Answers: map[string][]string{},
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			if !errors.Is(err, ErrOperationExists) {
				return sessionruntime.InteractionResponse{}, err
			}
			operation, found, err = flow.store.Load(ctx, operationID)
			if err != nil || !found {
				return sessionruntime.InteractionResponse{}, err
			}
		}
	}
	if !matchesEnvelope(operation, envelope) {
		return sessionruntime.InteractionResponse{}, ErrInvalidEnvelope
	}
	if operation.Phase == PhasePrepared {
		operation, err = flow.deliverInitial(ctx, operation)
		if err != nil {
			return sessionruntime.InteractionResponse{}, err
		}
	}
	return flow.awaitResponse(ctx, operation)
}

func (flow *Flow) deliverInitial(ctx context.Context, operation Operation) (Operation, error) {
	fenced := operation
	fenced.Phase = PhaseSendUnknown
	fenced.UpdatedAt = flow.now().UTC()
	fenced, changed, err := flow.store.CompareAndSwap(ctx, operation.ID, operation.Revision, fenced)
	if err != nil {
		return Operation{}, err
	}
	if !changed {
		current, found, loadErr := flow.store.Load(ctx, operation.ID)
		if loadErr != nil || !found {
			return Operation{}, loadErr
		}
		return current, nil
	}
	receipt, sendErr := flow.sender.Deliver(ctx, Delivery{
		OperationID: operation.ID, SessionID: operation.SessionID, MessageID: operation.MessageID,
		ProviderRequestID: operation.ProviderRequestID, ConversationID: operation.ConversationID,
		Surface: surfaceFor(operation),
	})
	if sendErr != nil || receipt.OperationID != operation.ID || receipt.CarrierMessageID <= 0 {
		return Operation{}, ErrSendUnknown
	}
	waiting := fenced
	waiting.Phase = PhaseWaiting
	waiting.CarrierMessageID = receipt.CarrierMessageID
	waiting.UpdatedAt = flow.now().UTC()
	waiting, changed, err = flow.store.CompareAndSwap(context.WithoutCancel(ctx), fenced.ID, fenced.Revision, waiting)
	if err != nil || !changed {
		return Operation{}, ErrSendUnknown
	}
	flow.signal(operation.ID)
	return waiting, nil
}

func (flow *Flow) awaitResponse(ctx context.Context, operation Operation) (sessionruntime.InteractionResponse, error) {
	deadline := operation.CreatedAt.Add(flow.timeout)
	remaining := deadline.Sub(flow.now())
	if remaining < 0 {
		remaining = 0
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	for {
		// Subscribe before rereading durable state so a callback transition can
		// never land between the read and the waiter registration.
		wake := flow.watch(operation.ID)
		var found bool
		var err error
		operation, found, err = flow.store.Load(context.WithoutCancel(ctx), operation.ID)
		if err != nil {
			return sessionruntime.InteractionResponse{}, err
		}
		if !found {
			return sessionruntime.InteractionResponse{}, ErrInvalidOperation
		}
		switch operation.Phase {
		case PhaseSendUnknown:
			return sessionruntime.InteractionResponse{}, ErrSendUnknown
		case PhaseProviderResponseUnknown:
			if response, ok := flow.takeSecretResponse(operation.ID); ok {
				return response, nil
			}
			return sessionruntime.InteractionResponse{}, ErrProviderResponseUnknown
		case PhaseResponseReady:
			return flow.handoffResponse(context.WithoutCancel(ctx), operation)
		case PhaseWaiting, PhaseWaitingText:
		case PhaseSecretDeletionUnknown:
			if !flow.secretIsInFlight(operation.ID) {
				return sessionruntime.InteractionResponse{}, ErrProviderResponseUnknown
			}
		case PhasePrepared:
			return sessionruntime.InteractionResponse{}, ErrSendUnknown
		default:
			return sessionruntime.InteractionResponse{}, ErrInvalidOperation
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return flow.cancelWaiting(operation.ID, "cancelled")
		case <-timer.C:
			return flow.cancelWaiting(operation.ID, "timeout")
		}
	}
}

func (flow *Flow) cancelWaiting(id, resolution string) (sessionruntime.InteractionResponse, error) {
	for {
		operation, found, err := flow.store.Load(context.Background(), id)
		if err != nil {
			return sessionruntime.InteractionResponse{}, err
		}
		if !found {
			return sessionruntime.InteractionResponse{}, ErrInvalidOperation
		}
		if operation.Phase == PhaseResponseReady {
			return flow.handoffResponse(context.Background(), operation)
		}
		if operation.Phase == PhaseProviderResponseUnknown {
			return sessionruntime.InteractionResponse{}, ErrProviderResponseUnknown
		}
		if operation.Phase != PhaseWaiting && operation.Phase != PhaseWaitingText {
			return sessionruntime.InteractionResponse{}, ErrSendUnknown
		}
		response := runtimeprotocol.InteractionResponse{ID: operation.ProviderRequestID, Outcome: runtimeprotocol.OutcomeCancelled}
		next := operation
		next.Phase = PhaseResponseReady
		next.Response = &response
		next.Resolution = resolution
		next.UpdatedAt = flow.now().UTC()
		updated, changed, err := flow.store.CompareAndSwap(context.Background(), id, operation.Revision, next)
		if err != nil {
			return sessionruntime.InteractionResponse{}, err
		}
		if changed {
			flow.signal(id)
			return flow.handoffResponse(context.Background(), updated)
		}
	}
}

func (flow *Flow) handoffResponse(ctx context.Context, operation Operation) (sessionruntime.InteractionResponse, error) {
	if operation.Response == nil {
		return sessionruntime.InteractionResponse{}, ErrInvalidOperation
	}
	next := operation
	next.Phase = PhaseProviderResponseUnknown
	next.UpdatedAt = flow.now().UTC()
	updated, changed, err := flow.store.CompareAndSwap(ctx, operation.ID, operation.Revision, next)
	if err != nil {
		return sessionruntime.InteractionResponse{}, err
	}
	if !changed {
		return sessionruntime.InteractionResponse{}, ErrProviderResponseUnknown
	}
	return cloneResponse(*updated.Response), nil
}

// HandleCallback implements telegramflow.CallbackExecutor. The caller has
// already authenticated, owner-checked and durably claimed the signed token;
// this method additionally enforces the exact interaction operation, session
// and carrier correlation before changing provider response state.
func (flow *Flow) HandleCallback(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	if flow == nil || ctx == nil || plan.Interaction == nil || !saneText(plan.Interaction.RequestID, 256) ||
		!telegramui.IsInteractionAction(plan.Action) || !saneText(plan.OperationID, 256) {
		return telegramflow.CallbackResult{}, ErrInvalidCallback
	}
	for {
		operation, found, err := flow.store.Load(ctx, plan.Interaction.RequestID)
		if err != nil {
			return telegramflow.CallbackResult{}, err
		}
		if !found || operation.SessionID != plan.SessionID || operation.ConversationID != plan.Carrier.ChatID ||
			operation.CarrierMessageID != plan.Carrier.MessageID {
			return telegramflow.CallbackResult{}, ErrStaleCallback
		}
		if operation.LastCallbackID == plan.OperationID {
			return callbackResult(operation, plan.OperationID), nil
		}
		if operation.Phase != PhaseWaiting && !(operation.Phase == PhaseWaitingText && plan.Action == telegramui.ActionInteractionCancel) {
			return telegramflow.CallbackResult{}, ErrStaleCallback
		}
		next, err := applyCallback(operation, plan)
		if err != nil {
			return telegramflow.CallbackResult{}, err
		}
		next.LastCallbackID = plan.OperationID
		next.UpdatedAt = flow.now().UTC()
		updated, changed, err := flow.store.CompareAndSwap(ctx, operation.ID, operation.Revision, next)
		if err != nil {
			return telegramflow.CallbackResult{}, err
		}
		if changed {
			flow.signal(operation.ID)
			return callbackResult(updated, plan.OperationID), nil
		}
	}
}

func applyCallback(operation Operation, plan telegrampipeline.CallbackPlan) (Operation, error) {
	next := cloneOperation(operation)
	switch plan.Action {
	case telegramui.ActionInteractionChoice:
		if operation.Request.Kind != runtimeprotocol.InteractionQuestion ||
			plan.Interaction.ChoiceIndex != plan.Target.InteractionChoice || operation.QuestionIndex >= len(operation.Request.Questions) {
			return Operation{}, ErrInvalidCallback
		}
		question := operation.Request.Questions[operation.QuestionIndex]
		choice := plan.Interaction.ChoiceIndex
		if choice < 1 || choice > len(question.Options) {
			return Operation{}, ErrInvalidCallback
		}
		next.Answers[question.ID] = []string{question.Options[choice-1].Label}
		next.QuestionIndex++
		if next.QuestionIndex == len(next.Request.Questions) {
			response := runtimeprotocol.InteractionResponse{
				ID: next.ProviderRequestID, Outcome: runtimeprotocol.OutcomeAnswered, Answers: cloneAnswers(next.Answers),
			}
			if runtimeprotocol.ValidateResponse(next.Request, response, runtimeprotocol.Limits{}) != nil {
				return Operation{}, ErrInvalidCallback
			}
			next.Response = &response
			next.Phase = PhaseResponseReady
			next.Resolution = "answered"
		}
	case telegramui.ActionInteractionOther:
		if operation.Request.Kind != runtimeprotocol.InteractionQuestion || operation.QuestionIndex >= len(operation.Request.Questions) {
			return Operation{}, ErrInvalidCallback
		}
		question := operation.Request.Questions[operation.QuestionIndex]
		if !question.IsOther || operation.QuestionIndex != len(operation.Request.Questions)-1 || plan.Interaction.ChoiceIndex != 0 {
			return Operation{}, ErrInvalidCallback
		}
		next.Phase = PhaseWaitingText
		next.Resolution = "awaiting_other"
	case telegramui.ActionInteractionAccept, telegramui.ActionInteractionDecline:
		if operation.Request.Kind != runtimeprotocol.InteractionCommandApproval && operation.Request.Kind != runtimeprotocol.InteractionFileApproval {
			return Operation{}, ErrInvalidCallback
		}
		decision := runtimeprotocol.DecisionAccept
		if plan.Action == telegramui.ActionInteractionDecline {
			decision = runtimeprotocol.DecisionDecline
		} else if !containsDecision(operation.Request.Decisions, runtimeprotocol.DecisionAccept) &&
			containsDecision(operation.Request.Decisions, runtimeprotocol.DecisionAcceptForSession) {
			decision = runtimeprotocol.DecisionAcceptForSession
		}
		response := runtimeprotocol.InteractionResponse{ID: operation.ProviderRequestID, Outcome: runtimeprotocol.OutcomeAnswered, Decision: decision}
		if runtimeprotocol.ValidateResponse(operation.Request, response, runtimeprotocol.Limits{}) != nil {
			return Operation{}, ErrInvalidCallback
		}
		next.Response = &response
		next.Phase = PhaseResponseReady
		next.Resolution = string(decision)
	case telegramui.ActionInteractionCancel:
		response := runtimeprotocol.InteractionResponse{ID: operation.ProviderRequestID, Outcome: runtimeprotocol.OutcomeCancelled}
		if runtimeprotocol.ValidateResponse(operation.Request, response, runtimeprotocol.Limits{}) != nil {
			return Operation{}, ErrInvalidCallback
		}
		next.Response = &response
		next.Phase = PhaseResponseReady
		next.Resolution = "cancelled"
	default:
		return Operation{}, ErrInvalidCallback
	}
	return next, nil
}

// ResumeOperation is an explicit recovery probe. Ambiguous terminal phases are
// observable but never replayed.
func (flow *Flow) ResumeOperation(ctx context.Context, id string) (sessionruntime.InteractionResponse, error) {
	operation, found, err := flow.store.Load(ctx, id)
	if err != nil {
		return sessionruntime.InteractionResponse{}, err
	}
	if !found {
		return sessionruntime.InteractionResponse{}, ErrInvalidOperation
	}
	switch operation.Phase {
	case PhaseResponseReady:
		return flow.handoffResponse(ctx, operation)
	case PhaseProviderResponseUnknown:
		return sessionruntime.InteractionResponse{}, ErrProviderResponseUnknown
	case PhaseSendUnknown:
		return sessionruntime.InteractionResponse{}, ErrSendUnknown
	default:
		return sessionruntime.InteractionResponse{}, ErrInvalidOperation
	}
}

// ConfirmInteractionResponse records the adapter's post-provider-write ack and
// prunes only after the confirmed phase is durable. Reopen prunes a confirmed
// record left by a crash between those two local writes.
func (flow *Flow) ConfirmInteractionResponse(ctx context.Context, acceptance telegramcontroller.InteractionResponseAcceptance) error {
	if flow == nil || ctx == nil || !saneText(string(acceptance.SessionID), 256) ||
		!saneText(acceptance.MessageID, 1024) || !saneText(acceptance.ProviderRequestID, 1024) {
		return ErrInvalidEnvelope
	}
	id := operationIDFor(acceptance.SessionID, acceptance.MessageID, acceptance.ProviderRequestID)
	for {
		operation, found, err := flow.store.Load(ctx, id)
		if err != nil {
			return err
		}
		if !found || operation.SessionID != acceptance.SessionID || operation.MessageID != acceptance.MessageID ||
			operation.ProviderRequestID != acceptance.ProviderRequestID {
			return ErrInvalidEnvelope
		}
		if operation.Phase == PhaseProviderResponseConfirmed {
			_, err := flow.store.DeleteConfirmed(ctx, id, operation.Revision)
			return err
		}
		if operation.Phase != PhaseProviderResponseUnknown {
			return ErrProviderResponseUnknown
		}
		next := cloneOperation(operation)
		next.Phase = PhaseProviderResponseConfirmed
		next.UpdatedAt = flow.now().UTC()
		confirmed, changed, err := flow.store.CompareAndSwap(ctx, id, operation.Revision, next)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		flow.dropSecretResponse(id)
		deleted, err := flow.store.DeleteConfirmed(ctx, id, confirmed.Revision)
		if err != nil {
			return err
		}
		if !deleted {
			continue
		}
		return nil
	}
}

func callbackResult(operation Operation, callbackOperationID string) telegramflow.CallbackResult {
	result := telegramflow.CallbackResult{OperationID: callbackOperationID}
	if operation.Phase == PhaseResponseReady || operation.Phase == PhaseProviderResponseUnknown {
		result.Terminal = &telegramflow.TerminalOutput{Text: "Ответ принят."}
		return result
	}
	surface := surfaceFor(operation)
	surface.InteractionSessionID = operation.SessionID
	surface.InteractionRequestID = operation.ID
	result.Surface = &surface
	return result
}

func surfaceFor(operation Operation) telegramflow.SurfaceOutput {
	keyboard := telegramui.CardKeyboard{}
	var text string
	if operation.Phase == PhaseWaitingText {
		question := operation.Request.Questions[operation.QuestionIndex]
		text = "Отправьте следующим сообщением другой ответ."
		if question.IsSecret {
			text += " Сообщение будет удалено до передачи провайдеру."
		}
		keyboard.Rows = append(keyboard.Rows, telegramui.ButtonRow{{Action: telegramui.ActionInteractionCancel}})
		return telegramflow.SurfaceOutput{Text: text, Keyboard: keyboard}
	}
	switch operation.Request.Kind {
	case runtimeprotocol.InteractionQuestion:
		if operation.QuestionIndex < len(operation.Request.Questions) {
			question := operation.Request.Questions[operation.QuestionIndex]
			var builder strings.Builder
			builder.WriteString(question.Header)
			builder.WriteString("\n\n")
			builder.WriteString(question.Text)
			for index, option := range question.Options {
				fmt.Fprintf(&builder, "\n\n%d. %s", index+1, option.Label)
				if option.Description != "" {
					builder.WriteString(" - ")
					builder.WriteString(option.Description)
				}
				keyboard.Rows = append(keyboard.Rows, telegramui.ButtonRow{{
					Action: telegramui.ActionInteractionChoice,
					Target: telegramui.ButtonTarget{InteractionChoice: index + 1},
				}})
			}
			if question.IsOther && operation.QuestionIndex == len(operation.Request.Questions)-1 {
				keyboard.Rows = append(keyboard.Rows, telegramui.ButtonRow{{Action: telegramui.ActionInteractionOther}})
			}
			text = builder.String()
		}
	case runtimeprotocol.InteractionCommandApproval:
		text = "Разрешить выполнение команды?\n\n" + operation.Request.Command
		keyboard.Rows = approvalRows(operation.Request)
	case runtimeprotocol.InteractionFileApproval:
		text = "Разрешить изменение файлов?\n\n" + operation.Request.GrantRoot
		keyboard.Rows = approvalRows(operation.Request)
	}
	keyboard.Rows = append(keyboard.Rows, telegramui.ButtonRow{{Action: telegramui.ActionInteractionCancel}})
	return telegramflow.SurfaceOutput{Text: boundedSurfaceText(text), Keyboard: keyboard}
}

func approvalRows(request runtimeprotocol.InteractionRequest) []telegramui.ButtonRow {
	rows := make([]telegramui.ButtonRow, 0, 1)
	row := make(telegramui.ButtonRow, 0, 2)
	if containsDecision(request.Decisions, runtimeprotocol.DecisionAccept) ||
		containsDecision(request.Decisions, runtimeprotocol.DecisionAcceptForSession) {
		row = append(row, telegramui.Button{Action: telegramui.ActionInteractionAccept})
	}
	if containsDecision(request.Decisions, runtimeprotocol.DecisionDecline) {
		row = append(row, telegramui.Button{Action: telegramui.ActionInteractionDecline})
	}
	if len(row) != 0 {
		rows = append(rows, row)
	}
	return rows
}

func containsDecision(decisions []runtimeprotocol.ApprovalDecision, decision runtimeprotocol.ApprovalDecision) bool {
	for _, candidate := range decisions {
		if candidate == decision {
			return true
		}
	}
	return false
}

func boundedSurfaceText(value string) string {
	if len(value) <= maxSurfaceTextBytes {
		return value
	}
	var builder strings.Builder
	for _, character := range value {
		if builder.Len()+utf8.RuneLen(character)+len("…") > maxSurfaceTextBytes {
			break
		}
		builder.WriteRune(character)
	}
	builder.WriteString("…")
	return builder.String()
}

func validEnvelope(envelope telegramcontroller.InteractionEnvelope) bool {
	if !saneText(string(envelope.SessionID), 256) || !saneText(envelope.MessageID, 1024) {
		return false
	}
	_, err := runtimeprotocol.EncodeAdapterLine(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeInteractionRequest,
		RequestID: "validation", InteractionRequest: &envelope.Request,
	}, runtimeprotocol.Limits{})
	return err == nil
}

func matchesEnvelope(operation Operation, envelope telegramcontroller.InteractionEnvelope) bool {
	return operation.SessionID == envelope.SessionID && operation.MessageID == envelope.MessageID &&
		operation.ProviderRequestID == envelope.Request.ID && reflect.DeepEqual(operation.Request, envelope.Request)
}

func operationID(envelope telegramcontroller.InteractionEnvelope) string {
	return operationIDFor(envelope.SessionID, envelope.MessageID, envelope.Request.ID)
}

func operationIDFor(sessionID domain.SessionID, messageID, providerRequestID string) string {
	digest := sha256.Sum256([]byte(string(sessionID) + "\x00" + messageID + "\x00" + providerRequestID))
	return "interaction:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func cloneResponse(response runtimeprotocol.InteractionResponse) runtimeprotocol.InteractionResponse {
	response.Answers = cloneAnswers(response.Answers)
	return response
}

func (flow *Flow) watch(id string) <-chan struct{} {
	flow.wakeMu.Lock()
	defer flow.wakeMu.Unlock()
	if flow.wake[id] == nil {
		flow.wake[id] = make(chan struct{})
	}
	return flow.wake[id]
}

func (flow *Flow) signal(id string) {
	flow.wakeMu.Lock()
	defer flow.wakeMu.Unlock()
	if current := flow.wake[id]; current != nil {
		close(current)
	}
	flow.wake[id] = make(chan struct{})
}

func (flow *Flow) secretIsInFlight(id string) bool {
	flow.secretMu.Lock()
	defer flow.secretMu.Unlock()
	return flow.secretInFlight[id]
}

func (flow *Flow) setSecretInFlight(id string, inFlight bool) {
	flow.secretMu.Lock()
	defer flow.secretMu.Unlock()
	if inFlight {
		flow.secretInFlight[id] = true
	} else {
		delete(flow.secretInFlight, id)
	}
}

func (flow *Flow) putSecretResponse(id string, response sessionruntime.InteractionResponse) {
	flow.secretMu.Lock()
	defer flow.secretMu.Unlock()
	flow.secretResponses[id] = cloneResponse(response)
}

func (flow *Flow) dropSecretResponse(id string) {
	flow.secretMu.Lock()
	defer flow.secretMu.Unlock()
	delete(flow.secretResponses, id)
}

func (flow *Flow) takeSecretResponse(id string) (sessionruntime.InteractionResponse, bool) {
	flow.secretMu.Lock()
	defer flow.secretMu.Unlock()
	response, ok := flow.secretResponses[id]
	delete(flow.secretResponses, id)
	return cloneResponse(response), ok
}

var _ telegramcontroller.InteractionHandler = (*Flow)(nil)
var _ telegramcontroller.InteractionTextHandler = (*Flow)(nil)
var _ telegramcontroller.InteractionAcceptanceHandler = (*Flow)(nil)
var _ telegramflow.CallbackExecutor = (*Flow)(nil)
