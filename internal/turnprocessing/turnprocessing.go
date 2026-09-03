// Package turnprocessing owns provider-neutral execution of one exact turn,
// including durable provider acceptance and structured attachment custody.
package turnprocessing

import (
	"context"
	"errors"
	"strings"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

type IncomingInput struct {
	Kind              string
	FileID            string
	FileUniqueID      string
	FileSize          int64
	MIMEType          string
	DurationSeconds   int
	Width             int
	Height            int
	DownloadPermitted bool
}

type InputPreparer interface {
	Prepare(context.Context, IncomingInput) (string, error)
}

type AttachmentRef struct {
	Reference string
	Size      int64
	SHA256    string
}
type PreparedInput struct {
	Text        string
	Attachments []AttachmentRef
}
type StructuredInputPreparer interface {
	PrepareStructured(context.Context, IncomingInput) (PreparedInput, error)
}
type PreparedTurnSubmitter interface {
	SubmitPreparedWithCallbacks(context.Context, domain.SessionID, PreparedInput, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)
}

type AttachmentReceipt struct{ Reference, ProviderSession, MessageID string }
type AttachmentCustody interface {
	MarkAccepted(context.Context, AttachmentReceipt) error
	MarkCompleted(context.Context, AttachmentReceipt) error
}

type SessionInput struct {
	SessionID   domain.SessionID
	MessageID   string
	Payload     []byte
	Attachments []AttachmentRef
}
type InputReceipt struct {
	Inserted  bool
	SessionID domain.SessionID
	MessageID string
	Sequence  uint64
}
type DurableLeasedInput struct {
	SessionID   domain.SessionID
	MessageID   string
	Sequence    uint64
	Payload     []byte
	Attachments []AttachmentRef
}
type DurableInputAcceptance struct {
	SessionID domain.SessionID
	MessageID string
	Sequence  uint64
}
type DurableInputCompletion string

const (
	DurableInputSucceeded DurableInputCompletion = "succeeded"
	DurableInputFailed    DurableInputCompletion = "failed"
)

type DurableInputCallbacks struct {
	OnAccepted func(context.Context, DurableInputAcceptance) error
}
type DurableInputProcessReceipt struct {
	SessionID  domain.SessionID
	MessageID  string
	Sequence   uint64
	Accepted   bool
	Completion DurableInputCompletion
}
type DurableInputCustody interface {
	Accept(context.Context, SessionInput) (InputReceipt, error)
}

type InteractionHandler interface {
	ResolveInteraction(context.Context, InteractionEnvelope) (sessionruntime.InteractionResponse, error)
}
type InteractionEnvelope struct {
	SessionID domain.SessionID
	MessageID string
	Request   sessionruntime.InteractionRequest
}
type InteractionResponseAcceptance struct {
	SessionID                    domain.SessionID
	MessageID, ProviderRequestID string
}
type InteractionAcceptanceHandler interface {
	ConfirmInteractionResponse(context.Context, InteractionResponseAcceptance) error
}
type InteractionTextInput struct {
	ActorID, ConversationID           int64
	ConversationKind                  string
	SourceMessageID, ReplyToMessageID int64
	Text, Caption, MediaKind          string
}
type InteractionTextResult struct {
	Handled               bool
	Status                string
	Secret, DeletionKnown bool
}
type InteractionTextHandler interface {
	ResolvePendingText(context.Context, InteractionTextInput) (InteractionTextResult, error)
}

type InteractionSourceTombstone interface {
	ConsumeBoundSourceMessage(context.Context, InteractionTextInput) (InteractionTextResult, error)
}

type Request struct {
	SessionID         domain.SessionID
	ProviderSessionID string
	MessageID         string
	Input             PreparedInput
}
type Callbacks struct {
	MarkInputAccepted func(context.Context) error
	AfterAccepted     func()
	OnEvent           func(sessionruntime.TurnEvent) error
}
type Execution struct {
	Result         sessionruntime.TurnResult
	Accepted       bool
	StreamedEvents bool
}

type RuntimeEventObservation struct {
	OperationID string
	SessionID   domain.SessionID
	MessageID   string
	EventIndex  int
	Event       sessionruntime.TurnEvent
}

// RuntimeEventObserver must be idempotent by OperationID.
type RuntimeEventObserver interface {
	ObserveRuntimeEvent(context.Context, RuntimeEventObservation) error
}

type FinalObservation struct {
	OperationID string
	SessionID   domain.SessionID
	MessageID   string
	Text        string
}

// FinalProcessor must be idempotent by OperationID.
type FinalProcessor interface {
	ProcessFinal(context.Context, FinalObservation) error
}

func Execute(ctx context.Context, submitter sessionruntime.Submitter, interactions InteractionHandler, attachments AttachmentCustody, request Request, callbacks Callbacks) (Execution, error) {
	if ctx == nil || submitter == nil || request.SessionID == "" || strings.TrimSpace(request.MessageID) == "" {
		return Execution{}, errors.New("turn processing request is invalid")
	}
	execution := Execution{StreamedEvents: true}
	turnCallbacks := sessionruntime.TurnCallbacks{
		MessageID: request.MessageID,
		OnAccepted: func(messageID string) error {
			if messageID != request.MessageID {
				return errors.New("provider accepted a different durable message")
			}
			if callbacks.MarkInputAccepted != nil {
				if err := callbacks.MarkInputAccepted(ctx); err != nil {
					return err
				}
			}
			for _, attachment := range request.Input.Attachments {
				if attachments == nil {
					return errors.New("attachment custody lifecycle is not configured")
				}
				if err := attachments.MarkAccepted(context.WithoutCancel(ctx), AttachmentReceipt{Reference: attachment.Reference, ProviderSession: request.ProviderSessionID, MessageID: request.MessageID}); err != nil {
					return err
				}
			}
			execution.Accepted = true
			if callbacks.AfterAccepted != nil {
				callbacks.AfterAccepted()
			}
			return nil
		},
		OnEvent: callbacks.OnEvent,
	}
	if interactions != nil {
		turnCallbacks.OnInteraction = func(ctx context.Context, interaction sessionruntime.InteractionRequest) (sessionruntime.InteractionResponse, error) {
			return interactions.ResolveInteraction(ctx, InteractionEnvelope{SessionID: request.SessionID, MessageID: request.MessageID, Request: interaction})
		}
		turnCallbacks.OnInteractionResponseAccepted = func(acceptance sessionruntime.InteractionResponseAcceptance) error {
			handler, ok := interactions.(InteractionAcceptanceHandler)
			if !ok {
				return errors.New("interaction acceptance handler is not configured")
			}
			if acceptance.ProviderSessionID != request.ProviderSessionID || acceptance.MessageID != request.MessageID || strings.TrimSpace(acceptance.InteractionID) == "" {
				return errors.New("provider confirmed a different interaction response")
			}
			return handler.ConfirmInteractionResponse(context.WithoutCancel(ctx), InteractionResponseAcceptance{SessionID: request.SessionID, MessageID: request.MessageID, ProviderRequestID: acceptance.InteractionID})
		}
	}
	var err error
	if len(request.Input.Attachments) != 0 {
		prepared, ok := submitter.(PreparedTurnSubmitter)
		if !ok {
			return execution, errors.New("provider does not support structured attachments")
		}
		execution.Result, err = prepared.SubmitPreparedWithCallbacks(ctx, request.SessionID, request.Input, turnCallbacks)
	} else if interactive, ok := submitter.(sessionruntime.InteractiveSubmitter); ok {
		execution.Result, err = interactive.SubmitWithCallbacks(ctx, request.SessionID, request.Input.Text, turnCallbacks)
	} else {
		execution.StreamedEvents = false
		execution.Result, err = submitter.Submit(ctx, request.SessionID, request.Input.Text)
	}
	return execution, err
}

func CompleteAttachments(ctx context.Context, custody AttachmentCustody, request Request) error {
	if len(request.Input.Attachments) == 0 {
		return nil
	}
	if ctx == nil || custody == nil || request.ProviderSessionID == "" || request.MessageID == "" {
		return errors.New("attachment completion request is invalid")
	}
	for _, attachment := range request.Input.Attachments {
		if err := custody.MarkCompleted(context.WithoutCancel(ctx), AttachmentReceipt{Reference: attachment.Reference, ProviderSession: request.ProviderSessionID, MessageID: request.MessageID}); err != nil {
			return err
		}
	}
	return nil
}
