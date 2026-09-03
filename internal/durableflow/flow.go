// Package durableflow coordinates provider-neutral input hand-off and output
// delivery through the durable message journal. It deliberately keeps provider
// and transport protocols outside this package.
package durableflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bria/internal/messagejournal"
)

var (
	ErrJournalRequired          = errors.New("durable flow journal is required")
	ErrInputProviderRequired    = errors.New("durable flow input provider is required")
	ErrOutputSenderRequired     = errors.New("durable flow output sender is required")
	ErrAcceptedResolverRequired = errors.New("accepted input resolver is required")
	ErrInvalidHandoff           = errors.New("invalid provider hand-off result")
	ErrInvalidDelivery          = errors.New("invalid output delivery result")
	ErrInvalidResolution        = errors.New("invalid accepted input resolution")
)

// InputProvider owns the provider-specific acceptance boundary. Accepted may
// be returned only after the provider has acknowledged this exact MessageID.
type InputProvider interface {
	Handoff(context.Context, ProviderInput) (HandoffResult, error)
}

type InputProcessState string

const (
	InputProcessCompleted InputProcessState = "completed"
	InputProcessFailed    InputProcessState = "failed"
	InputProcessUnknown   InputProcessState = "unknown"
)

type InputProcessCallbacks struct {
	// OnAccepted is the only path that may transition the leased input to
	// accepted. The receipt must be the exact tuple supplied to Process.
	OnAccepted func(context.Context, HandoffResult) error
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

// OutputSender owns one external delivery attempt. It must return Confirmed
// only with an unambiguous durable receipt.
type OutputSender interface {
	Deliver(context.Context, ProviderOutput) (DeliveryResult, error)
}

// Journal is the durable transition boundary used by Flow. The concrete
// messagejournal.Journal implements it; the interface also makes persistence
// failure behavior independently testable.
type Journal interface {
	EnqueueInput(context.Context, string, string, []byte) (messagejournal.Input, bool, error)
	EnqueueInputWithAttachments(context.Context, string, string, []byte, []messagejournal.AttachmentRef) (messagejournal.Input, bool, error)
	Inputs(context.Context, string) ([]messagejournal.Input, error)
	LeaseNextInput(context.Context, string, string, time.Time, time.Duration) (messagejournal.Input, error)
	MarkInputAccepted(context.Context, string, string, string) (messagejournal.Input, error)
	ReleaseInputLease(context.Context, string, string, string) (messagejournal.Input, error)
	MarkInputDeliveryFailed(context.Context, string, string, string) (messagejournal.Input, error)
	MarkInputDeliveryUnknown(context.Context, string, string, string) (messagejournal.Input, error)
	CompleteInput(context.Context, string, string) (messagejournal.Input, error)
	FailInput(context.Context, string, string) (messagejournal.Input, error)
	MarkInputUnknown(context.Context, string, string) (messagejournal.Input, error)
	RetryInput(context.Context, string, string) (messagejournal.Input, error)
	EnqueueOutput(context.Context, string, string, string, []byte) (messagejournal.Output, bool, error)
	LeaseNextOutput(context.Context, string, string, time.Time, time.Duration) (messagejournal.Output, error)
	ConfirmOutput(context.Context, string, string, string, string) (messagejournal.Output, error)
	MarkOutputFailed(context.Context, string, string, string) (messagejournal.Output, error)
	MarkOutputUnknown(context.Context, string, string, string) (messagejournal.Output, error)
	RetryOutput(context.Context, string, string) (messagejournal.Output, error)
}

type HandoffState string

const (
	// HandoffAccepted proves that the provider accepted the input.
	HandoffAccepted HandoffState = "accepted"
	// HandoffDeferred proves that no provider hand-off began and is safe to
	// attempt again automatically.
	HandoffDeferred HandoffState = "deferred"
	// HandoffRejected is a definite failed hand-off that requires an explicit
	// RetryInput before the ordered lane can continue.
	HandoffRejected HandoffState = "rejected"
	// HandoffUnknown is an ambiguous hand-off. It is never retried
	// automatically because the provider may already have accepted it.
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
	AcceptedCompleted AcceptedResolution = "completed"
	AcceptedFailed    AcceptedResolution = "failed"
	AcceptedUnknown   AcceptedResolution = "unknown"
)

// AcceptedInput is the exact durable identity requiring provider-history
// reconciliation after a process or machine restart.
type AcceptedInput struct {
	SessionID   string
	MessageID   string
	Sequence    uint64
	Payload     []byte
	Attachments []messagejournal.AttachmentRef
}

// AcceptedResolutionResult is an exact provider-history receipt. All identity
// fields must match the AcceptedInput supplied to the resolver.
type AcceptedResolutionResult struct {
	SessionID  string
	MessageID  string
	Sequence   uint64
	Resolution AcceptedResolution
}

type AcceptedInputResolver interface {
	ResolveAccepted(context.Context, AcceptedInput) (AcceptedResolutionResult, error)
}

// ProviderInput is an immutable copy of one leased journal input.
type ProviderInput struct {
	SessionID   string
	MessageID   string
	Sequence    uint64
	Payload     []byte
	Attachments []messagejournal.AttachmentRef
}

// HandoffResult is both the provider result and the durable transition receipt
// returned by DispatchNextInput.
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

// DeliveryResult is both the transport result and the durable transition
// receipt returned by DeliverNextOutput.
type DeliveryResult struct {
	SessionID   string
	OperationID string
	Sequence    uint64
	State       DeliveryState
	Receipt     string
}

// EnqueueReceipt proves that the journal accepted an identity and payload.
// Inserted is false for an exact idempotent replay.
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

// Flow is safe for concurrent use. The journal serializes short durable state
// transitions, while provider and sender calls run outside that lock so
// independent sessions do not block one another.
type Flow struct {
	journal       Journal
	provider      InputProvider
	sender        OutputSender
	owner         string
	leaseDuration time.Duration
	now           func() time.Time
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

// EnqueueInput is the only ingress acknowledgement boundary: success means the
// exact identity and payload survived the journal's write and reread probe.
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

// DispatchNextInput leases and hands off only the oldest unresolved item in a
// session lane. Deferred is safe to release; every failed or ambiguous outcome
// blocks the lane until explicit retry.
func (flow *Flow) DispatchNextInput(ctx context.Context, sessionID string) (HandoffResult, error) {
	if flow.provider == nil {
		return HandoffResult{}, ErrInputProviderRequired
	}
	input, err := flow.journal.LeaseNextInput(ctx, sessionID, flow.owner, flow.now(), flow.leaseDuration)
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
	// Once the external hand-off has begun, caller cancellation cannot be
	// allowed to return the item to automatic dispatch. Journal persistence is
	// local and bounded independently from the provider call.
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
		_, err = flow.RecordLeasedInputAccepted(custodyCtx, input.SessionID, input.MessageID, input.Sequence)
		if err != nil {
			// A provider acceptance without a durable transition must never be
			// allowed to return to automatic pending dispatch.
			result.State = HandoffUnknown
			sealErr := flow.markInputUnknown(custodyCtx, input)
			return result, errors.Join(err, sealErr)
		}
		return result, nil
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
	input, err := flow.journal.LeaseNextInput(ctx, sessionID, flow.owner, flow.now(), flow.leaseDuration)
	if err != nil {
		return InputProcessResult{}, err
	}
	request := ProviderInput{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Payload: append([]byte(nil), input.Payload...), Attachments: cloneAttachmentRefs(input.Attachments)}
	result := InputProcessResult{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, State: InputProcessUnknown}
	accepted := false
	callbacks := InputProcessCallbacks{OnAccepted: func(callbackCtx context.Context, receipt HandoffResult) error {
		if accepted || receipt.SessionID != input.SessionID || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence || receipt.State != HandoffAccepted {
			return ErrInvalidHandoff
		}
		persisted, persistErr := flow.RecordLeasedInputAccepted(callbackCtx, input.SessionID, input.MessageID, input.Sequence)
		if persistErr != nil || persisted.State != HandoffAccepted {
			return errors.Join(ErrInvalidHandoff, persistErr)
		}
		accepted = true
		return nil
	}}
	processed, processErr := processor.Process(ctx, request, callbacks)
	if processed.SessionID != input.SessionID || processed.MessageID != input.MessageID || processed.Sequence != input.Sequence {
		processErr = errors.Join(processErr, ErrInvalidHandoff)
	} else {
		result.State = processed.State
	}
	custodyCtx := context.WithoutCancel(ctx)
	if processErr != nil || !accepted {
		result.State = InputProcessUnknown
		var persistErr error
		if accepted {
			_, persistErr = flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
		} else {
			persistErr = flow.markInputUnknown(custodyCtx, input)
		}
		return result, errors.Join(processErr, persistErr)
	}
	switch result.State {
	case InputProcessCompleted:
		err = flow.RecordInputCompletionExact(custodyCtx, input.SessionID, input.MessageID, input.Sequence, CompletionSucceeded)
	case InputProcessFailed:
		err = flow.RecordInputCompletionExact(custodyCtx, input.SessionID, input.MessageID, input.Sequence, CompletionFailed)
	case InputProcessUnknown:
		_, err = flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
	default:
		result.State = InputProcessUnknown
		_, persistErr := flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
		return result, errors.Join(ErrInvalidHandoff, persistErr)
	}
	return result, err
}

// RecordLeasedInputAccepted durably seals the exact currently leased tuple.
// It is intended to be called directly from the provider's exact OnAccepted
// callback; a stale or mismatched sequence can never acknowledge another item.
func (flow *Flow) RecordLeasedInputAccepted(ctx context.Context, sessionID, messageID string, sequence uint64) (HandoffResult, error) {
	result := HandoffResult{SessionID: sessionID, MessageID: messageID, Sequence: sequence, State: HandoffUnknown}
	if flow == nil || flow.journal == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" || sequence == 0 {
		return result, ErrInvalidHandoff
	}
	inputs, err := flow.journal.Inputs(ctx, sessionID)
	if err != nil {
		return result, err
	}
	var leased *messagejournal.Input
	for index := range inputs {
		if inputs[index].MessageID == messageID {
			leased = &inputs[index]
			break
		}
	}
	if leased == nil || leased.Sequence != sequence {
		return result, ErrInvalidHandoff
	}
	if leased.Phase == messagejournal.InputAccepted || leased.Phase == messagejournal.InputCompleted || leased.Phase == messagejournal.InputFailed {
		result.State = HandoffAccepted
		return result, nil
	}
	if leased.Phase != messagejournal.InputPending || leased.Lease.Owner != flow.owner {
		return result, ErrInvalidHandoff
	}
	accepted, err := flow.journal.MarkInputAccepted(context.WithoutCancel(ctx), sessionID, messageID, flow.owner)
	if err != nil {
		return result, err
	}
	if accepted.SessionID != sessionID || accepted.MessageID != messageID || accepted.Sequence != sequence || accepted.Phase != messagejournal.InputAccepted {
		return result, ErrInvalidHandoff
	}
	result.State = HandoffAccepted
	return result, nil
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
	if flow == nil || flow.journal == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" || sequence == 0 {
		return ErrInvalidHandoff
	}
	inputs, err := flow.journal.Inputs(ctx, sessionID)
	if err != nil {
		return err
	}
	found := false
	for _, input := range inputs {
		if input.MessageID == messageID {
			if input.Sequence != sequence {
				return ErrInvalidHandoff
			}
			if completion == CompletionSucceeded && input.Phase == messagejournal.InputCompleted {
				return nil
			}
			if completion == CompletionFailed && input.Phase == messagejournal.InputFailed {
				return nil
			}
			if input.Phase != messagejournal.InputAccepted {
				return ErrInvalidHandoff
			}
			found = true
			break
		}
	}
	if !found {
		return ErrInvalidHandoff
	}
	return flow.RecordInputCompletion(context.WithoutCancel(ctx), sessionID, messageID, completion)
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
		if input.Phase != messagejournal.InputAccepted {
			continue
		}
		request := AcceptedInput{
			SessionID:   input.SessionID,
			MessageID:   input.MessageID,
			Sequence:    input.Sequence,
			Payload:     append([]byte(nil), input.Payload...),
			Attachments: cloneAttachmentRefs(input.Attachments),
		}
		resolved, resolveErr := resolver.ResolveAccepted(ctx, request)
		result := AcceptedResolutionResult{
			SessionID:  input.SessionID,
			MessageID:  input.MessageID,
			Sequence:   input.Sequence,
			Resolution: resolved.Resolution,
		}
		custodyCtx := context.WithoutCancel(ctx)
		if resolveErr != nil {
			result.Resolution = AcceptedUnknown
			_, persistErr := flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
			results = append(results, result)
			return results, errors.Join(resolveErr, persistErr)
		}
		if resolved.SessionID != input.SessionID || resolved.MessageID != input.MessageID || resolved.Sequence != input.Sequence {
			result.Resolution = AcceptedUnknown
			_, persistErr := flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
			results = append(results, result)
			return results, errors.Join(ErrInvalidResolution, persistErr)
		}

		switch resolved.Resolution {
		case AcceptedCompleted:
			_, err = flow.journal.CompleteInput(custodyCtx, input.SessionID, input.MessageID)
		case AcceptedFailed:
			_, err = flow.journal.FailInput(custodyCtx, input.SessionID, input.MessageID)
		case AcceptedUnknown:
			_, err = flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
		default:
			result.Resolution = AcceptedUnknown
			_, persistErr := flow.journal.MarkInputUnknown(custodyCtx, input.SessionID, input.MessageID)
			results = append(results, result)
			return results, errors.Join(ErrInvalidResolution, persistErr)
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
