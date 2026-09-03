// Package authflow defines the provider-neutral, Telegram-facing authorization
// workflow. It deliberately does not own provider availability settings:
// disabling a provider must not delete or alter its authorization.
package authflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	ErrInvalidRequest        = errors.New("invalid authorization request")
	ErrUnauthorized          = errors.New("authorization request is not from the owner private chat")
	ErrOperationNotFound     = errors.New("authorization operation not found")
	ErrOperationConflict     = errors.New("authorization operation conflicts with its binding")
	ErrAuthorizationRejected = errors.New("authorization was rejected")
	// ErrProviderRejected is returned by an Authenticator only when the
	// official provider authoritatively rejected the login. Transport errors
	// and timeouts must not wrap this sentinel.
	ErrProviderRejected         = errors.New("provider confirmed authorization rejection")
	ErrAuthorizationUnconfirmed = errors.New("authorization outcome is unconfirmed")
	ErrChallengeExpired         = errors.New("authorization challenge expired")
	ErrDeletionUnconfirmed      = errors.New("secret message deletion was not confirmed")
	ErrStateUnavailable         = errors.New("authorization state is unavailable")
	ErrProviderUnavailable      = errors.New("authorization provider is unavailable")
)

type Provider string

const (
	ProviderCodex  Provider = "codex"
	ProviderClaude Provider = "claude"
)

func (provider Provider) valid() bool {
	return provider == ProviderCodex || provider == ProviderClaude
}

// Status contains only safe workflow state. Provider errors, Telegram errors,
// credentials and user-supplied authorization values are never stored in it.
// Provider enabled/disabled state intentionally belongs to configuration, not
// to this authorization status.
type Status string

const (
	StatusStarting       Status = "starting"
	StatusAwaitingAction Status = "awaiting_action"
	StatusCompleting     Status = "completing"
	StatusAuthenticated  Status = "authenticated"
	StatusRejected       Status = "rejected"
	StatusExpired        Status = "expired"
)

type DeletionStatus string

const (
	DeletionNotRequired DeletionStatus = "not_required"
	DeletionPending     DeletionStatus = "pending"
	DeletionConfirmed   DeletionStatus = "confirmed"
	DeletionUnconfirmed DeletionStatus = "unconfirmed"
)

// SecretMessageReference is safe to persist. It identifies the Telegram
// message to delete but never contains the message body.
type SecretMessageReference struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

// Record is the complete durable state of an authorization operation. The
// schema has no field capable of holding the submitted secret or provider
// credential data.
type Record struct {
	OperationID            string                 `json:"operation_id"`
	Revision               uint64                 `json:"revision"`
	OwnerID                int64                  `json:"owner_id"`
	PrivateChatID          int64                  `json:"private_chat_id"`
	ComputerID             string                 `json:"computer_id"`
	Provider               Provider               `json:"provider"`
	ChallengeReference     string                 `json:"challenge_reference,omitempty"`
	Status                 Status                 `json:"status"`
	ExpiresAt              time.Time              `json:"expires_at,omitempty"`
	SubmissionOperationID  string                 `json:"submission_operation_id,omitempty"`
	SecretMessageReference SecretMessageReference `json:"secret_message_reference,omitempty"`
	Deletion               DeletionStatus         `json:"deletion"`
	CreatedAt              time.Time              `json:"created_at"`
	UpdatedAt              time.Time              `json:"updated_at"`
}

type StartRequest struct {
	OperationID      string
	ActorID          int64
	ChatID           int64
	ConversationKind string
	ComputerID       string
	Provider         Provider
}

type StartResult struct {
	OperationID        string
	ChallengeReference string
	Instruction        string
	ExpiresAt          time.Time
	Status             Status
	Replayed           bool
}

type SubmitRequest struct {
	OperationID           string
	SubmissionOperationID string
	ActorID               int64
	ChatID                int64
	ConversationKind      string
	MessageID             int64
	ComputerID            string
	Provider              Provider
	ChallengeReference    string
	Secret                SecretPayload
}

type SubmitResult struct {
	OperationID string
	Status      Status
	Deletion    DeletionStatus
	Replayed    bool
}

// PendingRequest identifies the exact owner-private conversation whose
// non-terminal authorization operations must be restored after a restart.
type PendingRequest struct {
	ActorID          int64
	ChatID           int64
	ConversationKind string
}

// PendingOperation contains only durable, non-secret correlation state.
type PendingOperation struct {
	OperationID        string
	ComputerID         string
	Provider           Provider
	ChallengeReference string
	Status             Status
}

type DiscardRequest struct {
	OperationID      string
	ActorID          int64
	ChatID           int64
	ConversationKind string
	MessageID        int64
}

type DeletionIntent struct {
	OperationID string         `json:"operation_id"`
	ActorID     int64          `json:"actor_id"`
	ChatID      int64          `json:"chat_id"`
	MessageID   int64          `json:"message_id"`
	Deletion    DeletionStatus `json:"deletion"`
}

type DiscardResult struct {
	Deletion DeletionStatus
}

// MessageRequest identifies a redelivered Telegram source message without
// carrying its body. It is safe to use before ordinary input routing.
type MessageRequest struct {
	ActorID          int64
	ChatID           int64
	ConversationKind string
	MessageID        int64
}

type MessageResult struct {
	Bound    bool
	Provider Provider
	Status   Status
	Deletion DeletionStatus
}

type BeginRequest struct {
	OperationID   string
	OwnerID       int64
	PrivateChatID int64
	ComputerID    string
	Provider      Provider
}

// BeginResult contains transient display text and a non-secret correlation
// reference. Authenticator implementations must return an instruction safe to
// display and must never put a credential or one-time response in either field.
type BeginResult struct {
	ChallengeReference string
	Instruction        string
	ExpiresAt          time.Time
}

type CompleteRequest struct {
	OperationID           string
	SubmissionOperationID string
	ComputerID            string
	Provider              Provider
	ChallengeReference    string
	Secret                SecretPayload
}

// Authenticator delegates to the official provider login mechanism on the
// selected computer. Implementations must treat operation IDs idempotently;
// they are the replay fence across process recovery.
type Authenticator interface {
	Begin(context.Context, BeginRequest) (BeginResult, error)
	Complete(context.Context, CompleteRequest) error
}

type TelegramDeleter interface {
	DeleteMessage(context.Context, int64, int64) error
}

type Store interface {
	Create(context.Context, Record) (Record, bool, error)
	Load(context.Context, string) (Record, bool, error)
	CompareAndSwap(context.Context, string, uint64, Record) (Record, bool, error)
}

// PendingStore is required for restart-safe Telegram secret routing. Listing
// is scoped by the immutable owner/private-chat binding and returns no secret.
type PendingStore interface {
	Store
	ListPending(context.Context, int64, int64) ([]Record, error)
}

type DeletionIntentStore interface {
	LoadDeletionIntent(context.Context, string) (DeletionIntent, bool, error)
	SaveDeletionIntent(context.Context, DeletionIntent) (DeletionIntent, error)
}

// BoundMessageStore finds durable authorization tombstones by their exact
// owner/private-chat/source-message identity, including terminal operations.
type BoundMessageStore interface {
	FindSubmissionByMessage(context.Context, int64, int64, int64) (Record, bool, error)
	FindDeletionIntentByMessage(context.Context, int64, int64, int64) (DeletionIntent, bool, error)
}

// MaintenanceStore bounds replay-fence growth without ever removing an
// in-progress authorization. Callers choose the retention cutoff explicitly.
type MaintenanceStore interface {
	Store
	PruneTerminalBefore(context.Context, time.Time) (int, error)
}

// SecretPayload owns an in-memory copy of a temporary authorization response.
// It refuses standard formatting and serialization. Bytes returns a copy for a
// provider adapter; callers should retain it for no longer than the call.
type SecretPayload struct {
	value []byte
}

func NewSecretPayload(value []byte) SecretPayload {
	return SecretPayload{value: append([]byte(nil), value...)}
}

func (payload SecretPayload) Bytes() []byte {
	return append([]byte(nil), payload.value...)
}

func (payload SecretPayload) Empty() bool {
	return len(payload.value) == 0
}

func (payload *SecretPayload) Destroy() {
	if payload == nil {
		return
	}
	for index := range payload.value {
		payload.value[index] = 0
	}
	payload.value = nil
}

func (SecretPayload) String() string   { return "[REDACTED]" }
func (SecretPayload) GoString() string { return "authflow.SecretPayload([REDACTED])" }

func (SecretPayload) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[REDACTED]")
}

func (SecretPayload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret payload cannot be serialized")
}

func (SecretPayload) MarshalText() ([]byte, error) {
	return nil, errors.New("secret payload cannot be serialized")
}

var _ json.Marshaler = SecretPayload{}

func normalized(value string) string { return strings.TrimSpace(value) }

func validateDeletionIntent(intent DeletionIntent) error {
	if normalized(intent.OperationID) == "" || len(intent.OperationID) > 256 || intent.ActorID <= 0 || intent.ChatID <= 0 || intent.MessageID <= 0 {
		return ErrInvalidRequest
	}
	switch intent.Deletion {
	case DeletionPending, DeletionConfirmed, DeletionUnconfirmed:
		return nil
	default:
		return ErrInvalidRequest
	}
}

func sameDeletionBinding(left, right DeletionIntent) bool {
	return left.OperationID == right.OperationID && left.ActorID == right.ActorID && left.ChatID == right.ChatID && left.MessageID == right.MessageID
}
