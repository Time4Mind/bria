// Package durableflow coordinates provider-neutral durable hand-off and delivery.
package durableflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"bria/internal/acceptedinput"
	"bria/internal/messagejournal"
)

var (
	ErrJournalRequired          = errors.New("durable flow journal is required")
	ErrInputProviderRequired    = errors.New("durable flow input provider is required")
	ErrOutputSenderRequired     = errors.New("durable flow output sender is required")
	ErrAcceptedResolverRequired = errors.New("accepted input resolver is required")
	ErrInvalidHandoff           = acceptedinput.ErrInvalidReceipt
	ErrInvalidDelivery          = errors.New("invalid output delivery result")
	ErrInvalidResolution        = errors.New("invalid accepted input resolution")
)

// InputProvider may return Accepted only for the exact acknowledged MessageID.
type InputProvider interface {
	Handoff(context.Context, ProviderInput) (HandoffResult, error)
}

type InputProcessState string

const (
	InputProcessAccepted       InputProcessState = "accepted"
	InputProcessDeferred       InputProcessState = "deferred"
	InputProcessCompleted      InputProcessState = "completed"
	InputProcessFailed         InputProcessState = "failed"
	InputProcessTerminalFailed InputProcessState = "terminal_failed"
	InputProcessUnknown        InputProcessState = "unknown"
)

type InputProcessCallbacks struct {
	// OnPrepared replaces only the payload of the exact leased pending input.
	OnPrepared func(context.Context, ProviderInput) error
	// OnAccepted requires the exact tuple supplied to Process.
	OnAccepted func(context.Context, HandoffResult) error
	// OnCompleted commits an exact result after acceptance.
	OnCompleted func(context.Context, InputProcessResult) error
}

type InputProcessResult struct {
	SessionID string
	MessageID string
	Sequence  uint64
	State     InputProcessState
}

type InputProcessor interface {
	Process(context.Context, ProviderInput, InputProcessCallbacks) (InputProcessResult, error)
}

// OutputSender returns Confirmed only with an unambiguous durable receipt.
type OutputSender interface {
	Deliver(context.Context, ProviderOutput) (DeliveryResult, error)
}

// Journal is Flow's durable transition boundary.
type Journal interface {
	EnqueueInput(context.Context, string, string, []byte) (messagejournal.Input, bool, error)
	EnqueueInputWithAttachments(context.Context, string, string, []byte, []messagejournal.AttachmentRef) (messagejournal.Input, bool, error)
	Inputs(context.Context, string) ([]messagejournal.Input, error)
	LeaseNextInput(context.Context, string, string, time.Time, time.Duration) (messagejournal.Input, error)
	ReplaceLeasedInputPayload(context.Context, string, string, string, uint64, []byte) (messagejournal.Input, error)
	MarkInputAccepted(context.Context, string, string, string) (messagejournal.Input, error)
	ReleaseInputLease(context.Context, string, string, string) (messagejournal.Input, error)
	MarkInputDeliveryFailed(context.Context, string, string, string) (messagejournal.Input, error)
	MarkInputDeliveryUnknown(context.Context, string, string, string) (messagejournal.Input, error)
	CompleteInput(context.Context, string, string) (messagejournal.Input, error)
	FailInput(context.Context, string, string) (messagejournal.Input, error)
	MarkInputUnknown(context.Context, string, string) (messagejournal.Input, error)
	ResolveAcceptedInput(context.Context, string, string, uint64, messagejournal.InputPhase) (messagejournal.Input, error)
	RetryInput(context.Context, string, string) (messagejournal.Input, error)
	EnqueueOutput(context.Context, string, string, string, []byte) (messagejournal.Output, bool, error)
	LeaseNextOutput(context.Context, string, string, time.Time, time.Duration) (messagejournal.Output, error)
	ConfirmOutput(context.Context, string, string, string, string) (messagejournal.Output, error)
	MarkOutputFailed(context.Context, string, string, string) (messagejournal.Output, error)
	MarkOutputUnknown(context.Context, string, string, string) (messagejournal.Output, error)
	SupersedePendingOutputs(context.Context, string, string, []string) ([]messagejournal.Output, error)
	RetryOutput(context.Context, string, string) (messagejournal.Output, error)
}

type HandoffState string

const (
	// HandoffAccepted proves exact provider acceptance.
	HandoffAccepted HandoffState = "accepted"
	// HandoffDeferred is safe to retry automatically.
	HandoffDeferred HandoffState = "deferred"
	// HandoffRejected requires explicit RetryInput.
	HandoffRejected HandoffState = "rejected"
	// HandoffUnknown may already have been accepted and is never auto-retried.
	HandoffUnknown HandoffState = "unknown"
)

type DeliveryState string

const (
	DeliveryConfirmed DeliveryState = "confirmed"
	DeliveryFailed    DeliveryState = "failed"
	DeliveryUnknown   DeliveryState = "unknown"
)

type Completion string

const (
	CompletionSucceeded Completion = "succeeded"
	CompletionFailed    Completion = "failed"
)

type AcceptedResolution string

const (
	AcceptedCompleted      AcceptedResolution = "completed"
	AcceptedFailed         AcceptedResolution = "failed"
	AcceptedTerminalFailed AcceptedResolution = "terminal_failed"
	AcceptedUnknown        AcceptedResolution = "unknown"
	AcceptedPending        AcceptedResolution = "accepted" // Proven acceptance, terminal still pending.
)

// AcceptedInput is the exact durable identity requiring history reconciliation.
type AcceptedInput struct {
	SessionID            string
	MessageID            string
	Sequence             uint64
	PreviouslyUnknown    bool
	PreviouslyFailed     bool
	PreviouslyUnaccepted bool // A leased attempt with no durable acceptance yet.
	Payload              []byte
	Attachments          []messagejournal.AttachmentRef
}

// AcceptedResolutionResult must match the supplied AcceptedInput identity.
type AcceptedResolutionResult struct {
	SessionID        string
	MessageID        string
	Sequence         uint64
	Resolution       AcceptedResolution
	AcceptanceProven bool // Exact native acceptance proof, required for a leased pending attempt.
}

type AcceptedInputResolver interface {
	ResolveAccepted(context.Context, AcceptedInput) (AcceptedResolutionResult, error)
}

// ProviderInput is one immutable leased journal input.
type ProviderInput struct {
	SessionID   string
	MessageID   string
	Sequence    uint64
	Payload     []byte
	Attachments []messagejournal.AttachmentRef
}

// HandoffResult is a provider result and durable transition receipt.
type HandoffResult struct {
	SessionID string
	MessageID string
	Sequence  uint64
	State     HandoffState
}

// ProviderOutput is an immutable copy of one leased journal output.
type ProviderOutput struct {
	SessionID   string
	OperationID string
	Sequence    uint64
	Kind        string
	Payload     []byte
}

// DeliveryResult is the transport result and durable transition receipt.
type DeliveryResult struct {
	SessionID   string
	OperationID string
	Sequence    uint64
	State       DeliveryState
	Receipt     string
}

// EnqueueReceipt proves journal acceptance; Inserted is false for exact replay.
type EnqueueReceipt struct {
	Inserted    bool
	SessionID   string
	MessageID   string
	OperationID string
	Sequence    uint64
}

type Options struct {
	Owner         string
	LeaseDuration time.Duration
	Now           func() time.Time
}

// Flow is concurrency-safe; external calls run outside journal transitions.
type Flow struct {
	acceptanceFence acceptedinput.Fence
	journal         Journal
	provider        InputProvider
	sender          OutputSender
	owner           string
	leaseDuration   time.Duration
	now             func() time.Time
}

func New(journal Journal, provider InputProvider, sender OutputSender, options Options) (*Flow, error) {
	if journal == nil {
		return nil, ErrJournalRequired
	}
	if strings.TrimSpace(options.Owner) == "" || options.Owner != strings.TrimSpace(options.Owner) {
		return nil, errors.New("durable flow owner is invalid")
	}
	if options.LeaseDuration <= 0 {
		return nil, errors.New("durable flow lease duration must be positive")
	}
	if options.Now == nil {
		return nil, errors.New("durable flow clock is required")
	}
	return &Flow{
		journal:       journal,
		provider:      provider,
		sender:        sender,
		owner:         options.Owner,
		leaseDuration: options.LeaseDuration,
		now:           options.Now,
	}, nil
}

// EnqueueInput success means identity and payload survived write and reread.
func (flow *Flow) EnqueueInput(ctx context.Context, sessionID, messageID string, payload []byte) (EnqueueReceipt, error) {
	input, inserted, err := flow.journal.EnqueueInput(ctx, sessionID, messageID, payload)
	return EnqueueReceipt{
		Inserted:  inserted,
		SessionID: input.SessionID,
		MessageID: input.MessageID,
		Sequence:  input.Sequence,
	}, err
}

func (flow *Flow) EnqueueInputWithAttachments(ctx context.Context, sessionID, messageID string, payload []byte, attachments []messagejournal.AttachmentRef) (EnqueueReceipt, error) {
	input, inserted, err := flow.journal.EnqueueInputWithAttachments(ctx, sessionID, messageID, payload, attachments)
	return EnqueueReceipt{Inserted: inserted, SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence}, err
}

// DispatchNextInput hands off only the oldest unresolved item in a session lane.
func (flow *Flow) DispatchNextInput(ctx context.Context, sessionID string) (HandoffResult, error) {
	if flow.provider == nil {
		return HandoffResult{}, ErrInputProviderRequired
	}
	input, err := flow.acceptanceFence.Lease(ctx, flow.journal, sessionID, flow.owner, flow.now(), flow.leaseDuration)
	if err != nil {
		return HandoffResult{}, err
	}
	request := ProviderInput{
		SessionID:   input.SessionID,
		MessageID:   input.MessageID,
		Sequence:    input.Sequence,
		Payload:     append([]byte(nil), input.Payload...),
		Attachments: cloneAttachmentRefs(input.Attachments),
	}
	provided, providerErr := flow.provider.Handoff(ctx, request)
	// Caller cancellation cannot release a possibly handed-off item.
	custodyCtx := context.WithoutCancel(ctx)
	result := HandoffResult{
		SessionID: input.SessionID,
		MessageID: input.MessageID,
		Sequence:  input.Sequence,
		State:     provided.State,
	}

	if providerErr != nil {
		result.State = HandoffUnknown
		persistErr := flow.markInputUnknown(custodyCtx, input)
		return result, errors.Join(providerErr, persistErr)
	}
	if provided.SessionID != input.SessionID || provided.MessageID != input.MessageID || provided.Sequence != input.Sequence {
		result.State = HandoffUnknown
		persistErr := flow.markInputUnknown(custodyCtx, input)
		return result, errors.Join(ErrInvalidHandoff, persistErr)
	}

	switch provided.State {
	case HandoffAccepted:
		flow.acceptanceFence.Observe(input.SessionID, input.MessageID, input.Sequence)
		_, err = flow.RecordLeasedInputAccepted(custodyCtx, input.SessionID, input.MessageID, input.Sequence)
		return result, err
	case HandoffDeferred:
		_, err = flow.journal.ReleaseInputLease(custodyCtx, input.SessionID, input.MessageID, flow.owner)
		return result, err
	case HandoffRejected:
		err = flow.markInputFailed(custodyCtx, input)
		return result, err
	case HandoffUnknown:
		err = flow.markInputUnknown(custodyCtx, input)
		return result, err
	default:
		result.State = HandoffUnknown
		persistErr := flow.markInputUnknown(custodyCtx, input)
		return result, errors.Join(ErrInvalidHandoff, persistErr)
	}
}

// ProcessNextInput owns the complete lease -> exact provider acceptance ->
// terminal transition. It is the production path for processors that expose
// acceptance while the turn is still running; DispatchNextInput remains for
// legacy handoff-only providers.
func (flow *Flow) ProcessNextInput(ctx context.Context, sessionID string, processor InputProcessor) (InputProcessResult, error) {
	if processor == nil {
		return InputProcessResult{}, ErrInputProviderRequired
	}
	input, err := flow.acceptanceFence.Lease(ctx, flow.journal, sessionID, flow.owner, flow.now(), flow.leaseDuration)
	if err != nil {
		return InputProcessResult{}, err
	}
	request := ProviderInput{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Payload: append([]byte(nil), input.Payload...), Attachments: cloneAttachmentRefs(input.Attachments)}
	result := InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: InputProcessUnknown}
	var accepted atomic.Bool
	var observed atomic.Bool
	var acceptanceErr atomic.Pointer[error]
	callbacks := InputProcessCallbacks{OnPrepared: func(callbackCtx context.Context, prepared ProviderInput) error {
		if prepared.SessionID != input.SessionID || prepared.MessageID != input.MessageID || prepared.Sequence != input.Sequence {
			return ErrInvalidHandoff
		}
		persisted, persistErr := flow.journal.ReplaceLeasedInputPayload(callbackCtx, input.SessionID, input.MessageID, flow.owner, input.Sequence, prepared.Payload)
		if persistErr != nil || persisted.Sequence != input.Sequence || !bytes.Equal(persisted.Payload, prepared.Payload) {
			return errors.Join(ErrInvalidHandoff, persistErr)
		}
		input.Payload = append([]byte(nil), prepared.Payload...)
		return nil
	}, OnAccepted: func(callbackCtx context.Context, receipt HandoffResult) error {
		if receipt.SessionID != input.SessionID || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence || receipt.State != HandoffAccepted || !observed.CompareAndSwap(false, true) {
			return ErrInvalidHandoff
		}
		flow.acceptanceFence.Observe(input.SessionID, input.MessageID, input.Sequence)
		persisted, persistErr := flow.RecordLeasedInputAccepted(callbackCtx, input.SessionID, input.MessageID, input.Sequence)
		if persistErr != nil || persisted.State != HandoffAccepted {
			persistErr = errors.Join(ErrInvalidHandoff, persistErr)
			acceptanceErr.Store(&persistErr)
			return persistErr
		}
		accepted.Store(true)
		return nil
	}, OnCompleted: func(callbackCtx context.Context, receipt InputProcessResult) error {
		if !accepted.Load() || receipt.SessionID != request.SessionID || receipt.MessageID != request.MessageID || receipt.Sequence != request.Sequence {
			return ErrInvalidHandoff
		}
		return flow.recordProcessOutcome(callbackCtx, receipt)
	}}
	processed, processErr := processor.Process(ctx, request, callbacks)
	if ackErr := acceptanceErr.Load(); ackErr != nil {
		processErr = errors.Join(processErr, *ackErr)
	}
	if processed.SessionID != input.SessionID || processed.MessageID != input.MessageID || processed.Sequence != input.Sequence {
		processErr = errors.Join(processErr, ErrInvalidHandoff)
	} else {
		result.State = processed.State
	}
	custodyCtx := context.WithoutCancel(ctx)
	if processErr == nil && result.State == InputProcessDeferred && !accepted.Load() {
		_, err := flow.journal.ReleaseInputLease(custodyCtx, input.SessionID, input.MessageID, flow.owner)
		return result, err
	}
	if processErr != nil || !accepted.Load() {
		result.State = InputProcessUnknown
		var persistErr error
		if observed.Load() {
			result.State = InputProcessAccepted
		} else {
			persistErr = flow.markInputUnknown(custodyCtx, input)
		}
		return result, errors.Join(processErr, persistErr)
	}
	if result.State == InputProcessAccepted {
		return result, nil
	}
	err = callbacks.OnCompleted(custodyCtx, result)
	if errors.Is(err, ErrInvalidHandoff) {
		result.State = InputProcessUnknown
		err = errors.Join(err, callbacks.OnCompleted(custodyCtx, result))
	}
	if err != nil || result.State == InputProcessUnknown {
		result.State = InputProcessAccepted
	}
	return result, err
}

// RecordLeasedInputAccepted durably seals the exact currently leased tuple.
// It is intended to be called directly from the provider's exact OnAccepted
// callback; a stale or mismatched sequence can never acknowledge another item.
func (flow *Flow) RecordLeasedInputAccepted(ctx context.Context, sessionID, messageID string, sequence uint64) (HandoffResult, error) {
	result := HandoffResult{SessionID: sessionID, MessageID: messageID, Sequence: sequence, State: HandoffUnknown}
	if flow == nil {
		return result, ErrInvalidHandoff
	}
	err := flow.acceptanceFence.Commit(ctx, flow.journal, sessionID, messageID, sequence, flow.owner)
	if err == nil {
		result.State = HandoffAccepted
	}
	return result, err
}

func (flow *Flow) markInputFailed(ctx context.Context, input messagejournal.Input) error {
	_, err := flow.journal.MarkInputDeliveryFailed(ctx, input.SessionID, input.MessageID, flow.owner)
	return err
}

func (flow *Flow) markInputUnknown(ctx context.Context, input messagejournal.Input) error {
	_, err := flow.journal.MarkInputDeliveryUnknown(ctx, input.SessionID, input.MessageID, flow.owner)
	return err
}

// RecordInputCompletion transfers an already accepted input to its terminal
// provider outcome. A failed outcome remains blocked until explicit retry.
func (flow *Flow) RecordInputCompletion(ctx context.Context, sessionID, messageID string, completion Completion) error {
	custodyCtx := context.WithoutCancel(ctx)
	switch completion {
	case CompletionSucceeded:
		_, err := flow.journal.CompleteInput(custodyCtx, sessionID, messageID)
		return err
	case CompletionFailed:
		_, err := flow.journal.FailInput(custodyCtx, sessionID, messageID)
		return err
	default:
		return fmt.Errorf("invalid input completion %q", completion)
	}
}

// RecordInputCompletionExact applies a terminal outcome only to the exact
// accepted tuple observed by the provider continuation.
func (flow *Flow) RecordInputCompletionExact(ctx context.Context, sessionID, messageID string, sequence uint64, completion Completion) error {
	state := InputProcessCompleted
	if completion == CompletionFailed {
		state = InputProcessFailed
	} else if completion != CompletionSucceeded {
		return ErrInvalidHandoff
	}
	return flow.recordProcessOutcome(ctx, InputProcessResult{SessionID: sessionID, MessageID: messageID, Sequence: sequence, State: state})
}

func (flow *Flow) recordProcessOutcome(ctx context.Context, result InputProcessResult) error {
	if flow == nil || flow.journal == nil || result.Sequence == 0 || result.State != InputProcessAccepted && result.State != InputProcessCompleted && result.State != InputProcessFailed && result.State != InputProcessUnknown && result.State != InputProcessTerminalFailed {
		return ErrInvalidHandoff
	}
	if result.State == InputProcessAccepted {
		return acceptedinput.VerifyAcceptance(context.WithoutCancel(ctx), flow.journal, result.SessionID, result.MessageID, result.Sequence)
	}
	_, err := flow.journal.ResolveAcceptedInput(context.WithoutCancel(ctx), result.SessionID, result.MessageID, result.Sequence, messagejournal.InputPhase(result.State))
	if errors.Is(err, messagejournal.ErrInvalidTransition) || errors.Is(err, messagejournal.ErrNotFound) {
		return errors.Join(ErrInvalidHandoff, err)
	}
	return err
}

// ReconcileAcceptedInputs resolves durable provider acceptances after restart.
// Unknown or unverifiable history remains observable and blocks replay.
func (flow *Flow) ReconcileAcceptedInputs(ctx context.Context, sessionID string, resolver AcceptedInputResolver) ([]AcceptedResolutionResult, error) {
	if resolver == nil {
		return nil, ErrAcceptedResolverRequired
	}
	inputs, err := flow.journal.Inputs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	results := make([]AcceptedResolutionResult, 0)
	for _, input := range inputs {
		unaccepted := input.Phase == messagejournal.InputPending && input.Lease.Owner != ""
		if !unaccepted && input.Phase != messagejournal.InputAccepted && input.Phase != messagejournal.InputUnknown && input.Phase != messagejournal.InputFailed {
			continue
		}
		request := AcceptedInput{
			PreviouslyUnaccepted: unaccepted,
			PreviouslyUnknown:    input.Phase == messagejournal.InputUnknown,
			PreviouslyFailed:     input.Phase == messagejournal.InputFailed,
			SessionID:            input.SessionID,
			MessageID:            input.MessageID,
			Sequence:             input.Sequence,
			Payload:              append([]byte(nil), input.Payload...),
			Attachments:          cloneAttachmentRefs(input.Attachments),
		}
		resolved, resolveErr := resolver.ResolveAccepted(ctx, request)
		result := AcceptedResolutionResult{
			SessionID:  input.SessionID,
			MessageID:  input.MessageID,
			Sequence:   input.Sequence,
			Resolution: resolved.Resolution,
		}
		custodyCtx := context.WithoutCancel(ctx)
		if resolveErr == nil && (resolved.SessionID != input.SessionID || resolved.MessageID != input.MessageID || resolved.Sequence != input.Sequence || resolved.Resolution != AcceptedCompleted && resolved.Resolution != AcceptedFailed && resolved.Resolution != AcceptedUnknown && resolved.Resolution != AcceptedTerminalFailed && !(resolved.Resolution == AcceptedPending && (input.Phase == messagejournal.InputAccepted || unaccepted && resolved.AcceptanceProven))) {
			resolveErr = ErrInvalidResolution
		}
		if unaccepted {
			if resolveErr != nil || resolved.Resolution == AcceptedUnknown || !resolved.AcceptanceProven {
				result.Resolution = AcceptedUnknown
				return append(results, result), resolveErr
			}
			if err := acceptedinput.Commit(custodyCtx, flow.journal, input.SessionID, input.MessageID, input.Sequence, input.Lease.Owner); err != nil {
				result.Resolution = AcceptedPending
				return append(results, result), err
			}
			input.Phase = messagejournal.InputAccepted
		}
		if resolveErr != nil {
			result.Resolution = AcceptedUnknown
			if input.Phase == messagejournal.InputAccepted {
				result.Resolution = AcceptedPending
			}
			_, persistErr := flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
			results = append(results, result)
			return results, errors.Join(resolveErr, persistErr)
		}
		outcome := messagejournal.InputPhase(resolved.Resolution)
		if resolved.Resolution == AcceptedPending {
			outcome = messagejournal.InputUnknown
		}
		_, err = flow.journal.ResolveAcceptedInput(custodyCtx, input.SessionID, input.MessageID, input.Sequence, outcome)
		if input.Phase == messagejournal.InputAccepted && result.Resolution == AcceptedUnknown {
			result.Resolution = AcceptedPending
		}
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

func cloneAttachmentRefs(input []messagejournal.AttachmentRef) []messagejournal.AttachmentRef {
	return append([]messagejournal.AttachmentRef(nil), input...)
}

// RetryInput is the explicit operator/user boundary for a failed or ambiguous
// provider hand-off or terminal provider failure.
func (flow *Flow) RetryInput(ctx context.Context, sessionID, messageID string) error {
	_, err := flow.journal.RetryInput(ctx, sessionID, messageID)
	return err
}

func (flow *Flow) EnqueueOutput(ctx context.Context, sessionID, operationID, kind string, payload []byte) (EnqueueReceipt, error) {
	output, inserted, err := flow.journal.EnqueueOutput(ctx, sessionID, operationID, kind, payload)
	return EnqueueReceipt{
		Inserted:    inserted,
		SessionID:   output.SessionID,
		OperationID: output.OperationID,
		Sequence:    output.Sequence,
	}, err
}

func (flow *Flow) SupersedePendingOutputs(ctx context.Context, sessionID, keepOperationID string, kinds []string) error {
	if flow == nil || flow.journal == nil {
		return ErrOutputSenderRequired
	}
	_, err := flow.journal.SupersedePendingOutputs(ctx, sessionID, keepOperationID, kinds)
	return err
}

// DeliverNextOutput attempts only the oldest unresolved output. An invalid or
// errored sender result is ambiguous and is durably sealed as Unknown.
func (flow *Flow) DeliverNextOutput(ctx context.Context, sessionID string) (DeliveryResult, error) {
	if flow.sender == nil {
		return DeliveryResult{}, ErrOutputSenderRequired
	}
	output, err := flow.journal.LeaseNextOutput(ctx, sessionID, flow.owner, flow.now(), flow.leaseDuration)
	if err != nil {
		return DeliveryResult{}, err
	}
	request := ProviderOutput{
		SessionID:   output.SessionID,
		OperationID: output.OperationID,
		Sequence:    output.Sequence,
		Kind:        output.Kind,
		Payload:     append([]byte(nil), output.Payload...),
	}
	delivered, deliveryErr := flow.sender.Deliver(ctx, request)
	// An external delivery may have happened even when its caller was
	// canceled. Seal the outcome before returning so it cannot be replayed.
	custodyCtx := context.WithoutCancel(ctx)
	result := DeliveryResult{
		SessionID:   output.SessionID,
		OperationID: output.OperationID,
		Sequence:    output.Sequence,
		State:       delivered.State,
		Receipt:     delivered.Receipt,
	}
	if deliveryErr != nil {
		result.State = DeliveryUnknown
		result.Receipt = ""
		persistErr := flow.markOutputUnknown(custodyCtx, output)
		return result, errors.Join(deliveryErr, persistErr)
	}
	if delivered.SessionID != output.SessionID || delivered.OperationID != output.OperationID || delivered.Sequence != output.Sequence {
		result.State = DeliveryUnknown
		result.Receipt = ""
		persistErr := flow.markOutputUnknown(custodyCtx, output)
		return result, errors.Join(ErrInvalidDelivery, persistErr)
	}
	if delivered.State != DeliveryConfirmed && delivered.Receipt != "" {
		result.State = DeliveryUnknown
		result.Receipt = ""
		persistErr := flow.markOutputUnknown(custodyCtx, output)
		return result, errors.Join(ErrInvalidDelivery, persistErr)
	}

	switch delivered.State {
	case DeliveryConfirmed:
		if strings.TrimSpace(delivered.Receipt) == "" {
			result.State = DeliveryUnknown
			result.Receipt = ""
			persistErr := flow.markOutputUnknown(custodyCtx, output)
			return result, errors.Join(ErrInvalidDelivery, persistErr)
		}
		_, err = flow.journal.ConfirmOutput(custodyCtx, output.SessionID, output.OperationID, flow.owner, delivered.Receipt)
		if err == nil {
			return result, nil
		}
		// The transport receipt is exact, but the durable confirmation did
		// not complete. Seal the operation as in-doubt so lease expiry can
		// never send the confirmed external write a second time.
		result.State = DeliveryUnknown
		result.Receipt = ""
		sealErr := flow.markOutputUnknown(custodyCtx, output)
		return result, errors.Join(err, sealErr)
	case DeliveryFailed:
		_, err = flow.journal.MarkOutputFailed(custodyCtx, output.SessionID, output.OperationID, flow.owner)
		return result, err
	case DeliveryUnknown:
		_, err = flow.journal.MarkOutputUnknown(custodyCtx, output.SessionID, output.OperationID, flow.owner)
		return result, err
	default:
		result.State = DeliveryUnknown
		result.Receipt = ""
		persistErr := flow.markOutputUnknown(custodyCtx, output)
		return result, errors.Join(ErrInvalidDelivery, persistErr)
	}
}

func (flow *Flow) markOutputUnknown(ctx context.Context, output messagejournal.Output) error {
	_, err := flow.journal.MarkOutputUnknown(ctx, output.SessionID, output.OperationID, flow.owner)
	return err
}

// RetryOutput is the sole automatic-delivery unblock boundary for Failed and
// Unknown outputs. The original operation identity and sequence are retained.
func (flow *Flow) RetryOutput(ctx context.Context, sessionID, operationID string) error {
	_, err := flow.journal.RetryOutput(ctx, sessionID, operationID)
	return err
}
