// Package telegramcontrolport defines storage-neutral session, output and authorization contracts.
package telegramcontrolport

import (
	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/settingsport"
	"context"
)

type SessionCreator interface {
	Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error)
}
type PendingSessionStart struct {
	Session domain.Session
	Outcome <-chan SessionStartOutcome
}
type SessionStartOutcome struct {
	Session    domain.Session
	Replayed   bool
	StartError error
	Err        error
}
type AsyncSessionCreator interface {
	BeginCreate(context.Context, app.ConfirmedSessionIntent) (PendingSessionStart, error)
}
type SessionStore interface {
	List(context.Context) ([]domain.Session, error)
	Load(context.Context, domain.SessionID) (domain.Session, error)
}
type SessionNamer interface {
	Begin(context.Context, domain.Session, string, string) func(string)
	Wait(context.Context) error
}
type RefreshingSessionNamer interface {
	SessionNamer
	BeginWithRefresh(context.Context, domain.Session, string, string, func(domain.SessionID)) func(string)
}
type Notifier interface {
	Notify(context.Context, Notification) error
}
type DeliveryState string

const DeliveryUnknown DeliveryState = "unknown"

type NotificationFailure struct {
	SessionID       domain.SessionID
	Kind            NotificationKind
	State           DeliveryState
	DurablyRecorded bool
}
type OutputFailureRecorder interface {
	RecordNotificationFailure(context.Context, NotificationFailure) error
}
type ActiveSessionStore interface {
	SetActiveSession(context.Context, domain.SessionID) error
}
type CardCarrierStore interface {
	SetCardCarrier(context.Context, domain.SessionID, int64, int64) error
}
type CardPageStore interface {
	SetCardPage(context.Context, domain.SessionID, int, int, string, bool) error
}
type CardHistoryStore interface {
	AppendCardHistory(context.Context, domain.SessionID, string) error
	LoadCardHistory(context.Context, domain.SessionID) ([]string, error)
}
type CardPromptStore interface {
	SetCardPrompt(context.Context, domain.SessionID, string, string) error
}
type ActiveSessionLoader interface {
	LoadActiveSession(context.Context) (domain.SessionID, error)
}
type Preferences = settingsport.Preferences
type PreferenceSnapshot = settingsport.Snapshot
type ProviderPreference = settingsport.ProviderPreference
type ProviderPreferences = settingsport.ProviderPreferences
type Lifecycle interface {
	Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error
}
type ArchivedResumer interface {
	Resume(context.Context, domain.SessionID) (domain.Session, error)
}
type SessionRecoverer interface {
	RecoverSession(context.Context, domain.SessionID) (domain.Session, error)
}
type AsyncArchivedResumer interface {
	BeginResume(context.Context, domain.SessionID) (PendingSessionStart, error)
}
type SessionCloser interface {
	Close(context.Context, domain.SessionID) (app.CloseSessionResult, error)
}
type InteractiveSessionCloser interface {
	BeginClose(context.Context, domain.SessionID) (app.CloseSessionResult, error)
}
type TurnLifecycle interface {
	Start(context.Context, domain.SessionID) (domain.Session, error)
	BeginStop(context.Context, domain.SessionID) (domain.Session, error)
	Finish(context.Context, domain.SessionID) (domain.Session, bool, error)
}
type NotificationKind string

const (
	NotificationCommentary   NotificationKind = "commentary"
	NotificationQuestion     NotificationKind = "question"
	NotificationFinal        NotificationKind = "final"
	NotificationError        NotificationKind = "error"
	NotificationPromptStatus NotificationKind = "prompt_status"
	NotificationNativeScreen NotificationKind = "native_screen"
)

type Notification struct {
	OperationID    string
	ConversationID int64
	SessionID      domain.SessionID
	Kind           NotificationKind
	Text           string
}
type OutgoingNotification struct {
	OperationID    string
	ConversationID int64
	SessionID      domain.SessionID
	Kind           NotificationKind
	Payload        []byte
}
type OutputReceipt struct {
	Inserted    bool
	SessionID   domain.SessionID
	OperationID string
	Sequence    uint64
}
type DurableOutputCustody interface {
	AcceptOutput(context.Context, OutgoingNotification) (OutputReceipt, error)
}
type OutputDeliveryWaiter interface {
	WaitOutputDelivery(context.Context, domain.SessionID, string) error
}
type SessionDeliveryGate interface {
	Lock(domain.SessionID) func()
}
type AuthorizationStart struct {
	OperationID      string
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
	ComputerID       domain.ComputerID
	Provider         domain.Provider
}
type AuthorizationChallenge struct {
	OperationID        string
	ComputerID         domain.ComputerID
	Provider           domain.Provider
	ChallengeReference string
	Instruction        string
}
type AuthorizationSecret struct {
	OperationID           string
	SubmissionOperationID string
	ActorID               int64
	PrivateChatID         int64
	ConversationKind      string
	SourceMessageID       int64
	ComputerID            domain.ComputerID
	Provider              domain.Provider
	ChallengeReference    string
	Secret                []byte
}
type AuthorizationResult struct {
	Authenticated bool
	DeletionKnown bool
}
type AuthorizationPendingLookup struct {
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
}
type PendingAuthorization struct {
	AuthorizationChallenge
	AcceptsSecret bool
}
type AuthorizationDiscard struct {
	OperationID      string
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
	SourceMessageID  int64
}
type AuthorizationMessageLookup struct {
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
	SourceMessageID  int64
}
type AuthorizationMessageBinding struct {
	Bound         bool
	Provider      domain.Provider
	Authenticated bool
	DeletionKnown bool
}
type AuthorizationFlow interface {
	SupportsAuthorization(domain.Provider) bool
	StartAuthorization(context.Context, AuthorizationStart) (AuthorizationChallenge, error)
	ConsumeAuthorizationMessage(context.Context, AuthorizationMessageLookup) (AuthorizationMessageBinding, error)
	PendingAuthorizations(context.Context, AuthorizationPendingLookup) ([]PendingAuthorization, error)
	SubmitAuthorization(context.Context, AuthorizationSecret) (AuthorizationResult, error)
	DiscardAuthorizationMessage(context.Context, AuthorizationDiscard) (AuthorizationResult, error)
}
