package telegramcontrolport

import (
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/sessioncreation"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramstatus"
	"bria/internal/turnprocessing"
	"context"
	"time"
)

type ModelCatalog interface {
	Models(context.Context, domain.ComputerID, domain.Provider) ([]settingsport.Model, error)
}

type AcceptedTurnObserver interface {
	ObserveAcceptedWithCallbacks(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error)
}

type Options struct {
	QueueLimit int
	Lifecycle  Lifecycle
	UIState    ActiveSessionStore
	Settings   Preferences
	Providers  ProviderPreferences
	Stopper    sessionruntime.TurnStopper
	// InputPreparer is required for downloadable voice/photo content. Document
	// preparation additionally requires AllowDocumentInput because documents
	// can carry arbitrary bytes.
	InputPreparer         turnprocessing.InputPreparer
	AllowDocumentInput    bool
	DurableInput          turnprocessing.DurableInputCustody
	DurableOutput         DurableOutputCustody
	Interactions          turnprocessing.InteractionHandler
	InteractionText       turnprocessing.InteractionTextHandler
	Authorization         AuthorizationFlow
	Attachments           turnprocessing.AttachmentCustody
	RuntimeEvents         turnprocessing.RuntimeEventObserver
	AcceptedObserver      AcceptedTurnObserver
	Finals                turnprocessing.FinalProcessor
	AsyncCreator          AsyncSessionCreator
	ArchivedResumer       ArchivedResumer
	Recoverer             SessionRecoverer
	AsyncResumer          AsyncArchivedResumer
	SessionCloser         SessionCloser
	ControllerObserver    controllertelemetry.Observer
	TurnLifecycle         TurnLifecycle
	OutputFailures        OutputFailureRecorder
	Recovered             []domain.Session
	CreationEnvironment   sessioncreation.Environment
	Quotas                telegramstatus.Reader
	Models                ModelCatalog
	Native                sessionruntime.NativeController
	Preprocessor          promptpreprocess.Processor
	SessionNamer          SessionNamer
	PreprocessingObserver promptpreprocess.Observer
	PreprocessingTimeout  time.Duration
}
