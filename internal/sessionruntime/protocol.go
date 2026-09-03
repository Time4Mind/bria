package sessionruntime

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/runtimeprotocol"
)

const ProtocolVersion = 1

const ReadinessProtocol = "protocol"

// Adapter startup context is transported through a reserved, sanitized
// environment. Adapters must reject ambiguous combinations themselves too.
const (
	EnvironmentStartMode              = "BRIA_START_MODE"
	EnvironmentProviderSession        = "BRIA_PROVIDER_SESSION_ID"
	EnvironmentGeneration             = "BRIA_GENERATION"
	EnvironmentProviderCredentialFile = runtimeprotocol.EnvironmentProviderCredentialFile
)

type AuthenticationState string

const (
	AuthenticationUnknown   AuthenticationState = "unknown"
	AuthenticationConfirmed AuthenticationState = "confirmed"
	AuthenticationRejected  AuthenticationState = "rejected"
)

var (
	ErrTurnInFlight                 = errors.New("session already has a turn in flight")
	ErrTurnFailed                   = errors.New("provider turn failed")
	ErrProtocol                     = errors.New("adapter protocol violation")
	ErrTextTooLarge                 = errors.New("text exceeds configured limit")
	ErrEventHandler                 = errors.New("turn event handler failed")
	ErrInteractionHandler           = errors.New("provider interaction handler failed")
	ErrInteractionAcceptanceHandler = errors.New("provider interaction acceptance handler failed")
)

type EventKind string

const (
	EventCommentary EventKind = "commentary"
	EventQuestion   EventKind = "question"
)

const (
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

const (
	ErrorAuthenticationFailed = "authentication_failed"
	ErrorProvider             = "provider_error"
	ErrorProtocol             = "protocol_error"
	ErrorTransport            = "transport_error"
	ErrorInterrupted          = "interrupted"
)

// TurnEvent is one ordered, safe-to-display non-final provider event.
type TurnEvent struct {
	Kind EventKind
	Text string
}

// TurnResult is publishable only when Submit returns a nil error. On every
// failed or interrupted turn Final is empty even if an adapter sent a final
// candidate before its terminal failure.
type TurnResult struct {
	Events         []TurnEvent
	Final          string
	TerminalStatus string
	ErrorCode      string
}

// InteractionRequest and InteractionResponse deliberately reuse the bounded
// provider-neutral wire contract rather than exposing provider-specific JSON.
type InteractionRequest = runtimeprotocol.InteractionRequest
type InteractionResponse = runtimeprotocol.InteractionResponse

type InteractionResponseAcceptance struct {
	ProviderSessionID string
	MessageID         string
	InteractionID     string
}

type AcceptedTurnOutcome string

const (
	AcceptedTurnCompleted AcceptedTurnOutcome = "completed"
	AcceptedTurnFailed    AcceptedTurnOutcome = "failed"
	AcceptedTurnUnknown   AcceptedTurnOutcome = "unknown"
)

type ReconciledAcceptedTurn struct {
	MessageID string
	Outcome   AcceptedTurnOutcome
}

type AcceptedTurnReconciliation struct {
	Turns []ReconciledAcceptedTurn
}

type AcceptedTurnReadRequest struct {
	SessionID domain.SessionID
	Provider  domain.Provider
	Workdir   string
	Binding   domain.ProviderBinding
}

type AcceptedTurnReader interface {
	ReadAcceptedTurns(context.Context, AcceptedTurnReadRequest) (AcceptedTurnReconciliation, error)
}

// TurnCallbacks exposes events as they arrive and resolves one correlated
// provider interaction while the turn is paused. Callbacks run serially in
// wire order; returning an error fails closed and retires the adapter.
type TurnCallbacks struct {
	// MessageID is the durable upstream identity echoed by the adapter's
	// acceptance receipt. Empty preserves the legacy in-memory Submit seam.
	MessageID                     string
	OnAccepted                    func(string) error
	OnEvent                       func(TurnEvent) error
	OnInteraction                 func(context.Context, InteractionRequest) (InteractionResponse, error)
	OnInteractionResponseAccepted func(InteractionResponseAcceptance) error
}

// Submitter is the narrow provider-neutral turn API used by composition.
type Submitter interface {
	Submit(ctx context.Context, sessionID domain.SessionID, text string) (TurnResult, error)
}

// InteractiveSubmitter is the richer provider-neutral seam used by callers
// that can stream commentary and render provider questions or approvals.
type InteractiveSubmitter interface {
	SubmitWithCallbacks(ctx context.Context, sessionID domain.SessionID, text string, callbacks TurnCallbacks) (TurnResult, error)
}

type LocalAttachment = runtimeprotocol.LocalAttachment
type StructuredInput struct {
	Text        string
	Attachments []LocalAttachment
}
type StructuredSubmitter interface {
	SubmitStructuredWithCallbacks(context.Context, domain.SessionID, StructuredInput, TurnCallbacks) (TurnResult, error)
}

// TurnStopper confirms the active provider turn reached its correlated
// interrupted terminal before returning success.
type TurnStopper interface {
	StopCurrent(ctx context.Context, sessionID domain.SessionID) error
}

// ProcessSupervisor observes the lifetime of one exact adapter generation.
// A nil result means that generation has been physically reaped.
type ProcessSupervisor interface {
	Wait(ctx context.Context, sessionID domain.SessionID, binding domain.ProviderBinding) error
}
