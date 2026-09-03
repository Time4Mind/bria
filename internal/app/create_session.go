package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"bria/internal/domain"
)

var ErrIntentConflict = errors.New("confirmed session intent conflicts with its persisted session")

var ErrComputerNotLocal = errors.New("confirmed session computer is not local")

var ErrOutcomeUnknown = errors.New("create session durable outcome is unknown")

// ConfirmedSessionIntent contains the immutable choices confirmed by the user.
// IntentID must be reused when the same confirmation is replayed.
type ConfirmedSessionIntent struct {
	IntentID   domain.IntentID
	ComputerID domain.ComputerID
	Provider   domain.Provider
	Workdir    string
}

// SessionStartMode distinguishes creation of a provider session from exact
// continuation of an already identified provider session.
type SessionStartMode string

const (
	SessionStartNew    SessionStartMode = "new"
	SessionStartResume SessionStartMode = "resume"
)

// StartSessionRequest is the provider-neutral request for one local process.
type StartSessionRequest struct {
	SessionID    domain.SessionID
	ComputerID   domain.ComputerID
	Provider     domain.Provider
	Workdir      string
	Mode         SessionStartMode
	PriorBinding *domain.ProviderBinding
}

// Validate rejects ambiguous launches so an exact resume can never silently
// fall back to creation of a replacement provider session.
func (request StartSessionRequest) Validate() error {
	if strings.TrimSpace(string(request.SessionID)) == "" {
		return errors.New("logical session id is required")
	}
	if strings.TrimSpace(string(request.ComputerID)) == "" {
		return errors.New("computer id is required")
	}
	if request.Provider != domain.ProviderCodex && request.Provider != domain.ProviderClaude {
		return fmt.Errorf("unsupported provider %q", request.Provider)
	}
	if strings.TrimSpace(request.Workdir) == "" {
		return errors.New("workdir is required")
	}
	switch request.Mode {
	case SessionStartNew:
		if request.PriorBinding != nil {
			return errors.New("new session start cannot carry a prior provider binding")
		}
	case SessionStartResume:
		if request.PriorBinding == nil {
			return errors.New("exact resume requires the prior provider binding")
		}
		if request.PriorBinding.Provider != request.Provider {
			return fmt.Errorf("prior provider %q does not match requested provider %q", request.PriorBinding.Provider, request.Provider)
		}
		if strings.TrimSpace(request.PriorBinding.SessionID) == "" {
			return errors.New("prior provider session id is required")
		}
		if request.PriorBinding.Generation == 0 {
			return errors.New("prior provider generation must be greater than zero")
		}
	default:
		return fmt.Errorf("unsupported session start mode %q", request.Mode)
	}
	return nil
}

// CreateSessionResult is a confirmed durable creation outcome. StartError is
// non-nil only when the session was durably moved to awaiting recovery.
type CreateSessionResult struct {
	Session    domain.Session
	Replayed   bool
	StartError error
}

// SessionIDSource supplies stable logical session identifiers.
type SessionIDSource interface {
	NewSessionID(context.Context) (domain.SessionID, error)
}

// WorkdirValidator checks whether a confirmed working directory can be used
// before a session ID is allocated or any durable/external operation begins.
type WorkdirValidator interface {
	Validate(context.Context, string) error
}

// SessionStore owns durable session persistence. PutStartingIfAbsent must be
// atomic by IntentID. CompareAndSwap must durably replace expected with next or
// return an error without overwriting a different state.
type SessionStore interface {
	GetByIntent(context.Context, domain.IntentID) (domain.Session, bool, error)
	PutStartingIfAbsent(
		context.Context,
		domain.Session,
	) (stored domain.Session, inserted bool, err error)
	CompareAndSwap(context.Context, domain.Session, domain.Session) error
	Load(context.Context, domain.SessionID) (domain.Session, error)
}

// SessionStarter starts one long-lived provider adapter process and returns its
// exact binding. A Start error guarantees that the directly launched adapter
// process does not remain. Abort succeeds only after confirming that this
// adapter process no longer exists; an adapter owns the lifetime of any
// provider descendants it creates.
type SessionStarter interface {
	Start(context.Context, StartSessionRequest) (domain.ProviderBinding, error)
	Abort(context.Context, StartSessionRequest, domain.ProviderBinding) error
}

// SessionCreator creates one logical session for one confirmed intent.
type SessionCreator struct {
	localComputerID domain.ComputerID
	workdirs        WorkdirValidator
	ids             SessionIDSource
	store           SessionStore
	starter         SessionStarter
	clock           func() time.Time
	lifetime        domain.SessionLifetime
}

type SessionCreatorOption func(*SessionCreator) error

func WithSessionLifetime(lifetime domain.SessionLifetime) SessionCreatorOption {
	return func(creator *SessionCreator) error {
		if err := domain.ValidateSessionLifetime(lifetime); err != nil {
			return err
		}
		creator.lifetime = lifetime
		return nil
	}
}

func WithSessionClock(clock func() time.Time) SessionCreatorOption {
	return func(creator *SessionCreator) error {
		if clock == nil {
			return errors.New("session clock is required")
		}
		creator.clock = clock
		return nil
	}
}

func NewSessionCreator(
	localComputerID domain.ComputerID,
	workdirs WorkdirValidator,
	ids SessionIDSource,
	store SessionStore,
	starter SessionStarter,
	options ...SessionCreatorOption,
) (*SessionCreator, error) {
	if strings.TrimSpace(string(localComputerID)) == "" {
		return nil, errors.New("local computer id is required")
	}
	if workdirs == nil || ids == nil || store == nil || starter == nil {
		return nil, errors.New("session creator dependencies are required")
	}
	creator := &SessionCreator{
		localComputerID: localComputerID,
		workdirs:        workdirs,
		ids:             ids,
		store:           store,
		starter:         starter,
		clock:           func() time.Time { return time.Now().UTC() },
		lifetime:        domain.SessionLifetimeNever,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("session creator option is required")
		}
		if err := option(creator); err != nil {
			return nil, fmt.Errorf("configure session creator: %w", err)
		}
	}
	return creator, nil
}

func (creator *SessionCreator) Create(
	ctx context.Context,
	intent ConfirmedSessionIntent,
) (CreateSessionResult, error) {
	if err := domain.ValidateSessionIntent(
		intent.IntentID,
		intent.ComputerID,
		intent.Provider,
		intent.Workdir,
	); err != nil {
		return CreateSessionResult{}, fmt.Errorf("validate confirmed session intent: %w", err)
	}
	if intent.ComputerID != creator.localComputerID {
		return CreateSessionResult{}, fmt.Errorf(
			"%w: got %q, local %q",
			ErrComputerNotLocal,
			intent.ComputerID,
			creator.localComputerID,
		)
	}

	stored, exists, err := creator.store.GetByIntent(ctx, intent.IntentID)
	if err != nil {
		return CreateSessionResult{}, fmt.Errorf("load confirmed session intent: %w", err)
	}
	if exists {
		if !sameConfirmedIntent(stored, intent) {
			return CreateSessionResult{}, fmt.Errorf(
				"%w: intent %q",
				ErrIntentConflict,
				intent.IntentID,
			)
		}
		return CreateSessionResult{Session: stored, Replayed: true}, nil
	}

	if err := creator.workdirs.Validate(ctx, intent.Workdir); err != nil {
		return CreateSessionResult{}, fmt.Errorf("validate confirmed workdir: %w", err)
	}

	sessionID, err := creator.ids.NewSessionID(ctx)
	if err != nil {
		return CreateSessionResult{}, fmt.Errorf("allocate session id: %w", err)
	}

	starting, err := domain.NewStartingSessionAt(
		sessionID,
		intent.IntentID,
		intent.ComputerID,
		intent.Provider,
		intent.Workdir,
		creator.clock(),
		creator.lifetime,
	)
	if err != nil {
		return CreateSessionResult{}, fmt.Errorf("validate confirmed session intent: %w", err)
	}

	stored, inserted, err := creator.store.PutStartingIfAbsent(ctx, starting)
	if err != nil {
		return CreateSessionResult{}, fmt.Errorf("persist starting session: %w", err)
	}
	if !inserted {
		if !sameConfirmedIntent(stored, intent) {
			return CreateSessionResult{}, fmt.Errorf(
				"%w: intent %q",
				ErrIntentConflict,
				intent.IntentID,
			)
		}
		return CreateSessionResult{Session: stored, Replayed: true}, nil
	}
	if !sameConfirmedIntent(stored, intent) || stored.Status() != domain.SessionStarting {
		return CreateSessionResult{}, errors.New("session store returned a different inserted session")
	}

	startRequest := StartSessionRequest{
		SessionID:  stored.ID(),
		ComputerID: stored.ComputerID(),
		Provider:   stored.Provider(),
		Workdir:    stored.Workdir(),
		Mode:       SessionStartNew,
	}
	binding, startErr := creator.starter.Start(ctx, startRequest)
	if startErr == nil {
		ready, transitionErr := stored.Ready(binding)
		if transitionErr != nil {
			if abortErr := creator.starter.Abort(ctx, startRequest, binding); abortErr != nil {
				return CreateSessionResult{Session: stored}, errors.Join(
					ErrOutcomeUnknown,
					fmt.Errorf("invalid provider binding: %w", transitionErr),
					fmt.Errorf("abort invalid provider start: %w", abortErr),
				)
			}
			return creator.persistStartFailure(
				ctx,
				stored,
				fmt.Errorf("invalid provider binding: %w", transitionErr),
			)
		}
		return creator.persistReady(ctx, stored, ready, startRequest, binding)
	}
	return creator.persistStartFailure(ctx, stored, startErr)
}

func (creator *SessionCreator) persistReady(
	ctx context.Context,
	starting domain.Session,
	ready domain.Session,
	request StartSessionRequest,
	binding domain.ProviderBinding,
) (CreateSessionResult, error) {
	casErr := creator.store.CompareAndSwap(ctx, starting, ready)
	persisted, loadErr := creator.store.Load(ctx, starting.ID())
	if loadErr == nil && persisted.Equal(ready) {
		return CreateSessionResult{Session: persisted}, nil
	}
	if loadErr != nil {
		return CreateSessionResult{Session: starting}, errors.Join(
			ErrOutcomeUnknown,
			fmt.Errorf("reread ready session: %w", loadErr),
			casErr,
		)
	}
	if !persisted.Equal(starting) {
		return CreateSessionResult{Session: persisted}, errors.Join(
			ErrOutcomeUnknown,
			errors.New("ready transition reread a conflicting session state"),
			casErr,
		)
	}
	if abortErr := creator.starter.Abort(ctx, request, binding); abortErr != nil {
		return CreateSessionResult{Session: persisted}, errors.Join(
			ErrOutcomeUnknown,
			fmt.Errorf("abort unpersisted provider start: %w", abortErr),
			casErr,
		)
	}
	if casErr == nil {
		casErr = errors.New("ready transition was not visible after successful compare-and-swap")
	}
	return creator.persistStartFailure(
		ctx,
		starting,
		fmt.Errorf("persist ready session: %w", casErr),
	)
}

func (creator *SessionCreator) persistStartFailure(
	ctx context.Context,
	starting domain.Session,
	startErr error,
) (CreateSessionResult, error) {
	awaitingRecovery, err := starting.AwaitRecovery()
	if err != nil {
		return CreateSessionResult{Session: starting}, fmt.Errorf("prepare awaiting-recovery session: %w", err)
	}
	casErr := creator.store.CompareAndSwap(ctx, starting, awaitingRecovery)
	persisted, loadErr := creator.store.Load(ctx, starting.ID())
	if loadErr == nil && persisted.Equal(awaitingRecovery) {
		return CreateSessionResult{
			Session:    persisted,
			StartError: startErr,
		}, nil
	}
	if loadErr != nil {
		return CreateSessionResult{Session: starting}, errors.Join(
			ErrOutcomeUnknown,
			fmt.Errorf("reread awaiting-recovery session: %w", loadErr),
			casErr,
		)
	}
	if casErr == nil {
		casErr = errors.New("awaiting-recovery transition was not visible after successful compare-and-swap")
	}
	return CreateSessionResult{Session: persisted}, errors.Join(
		fmt.Errorf("persist awaiting-recovery session: %w", casErr),
		fmt.Errorf("reread session status %q, want %q", persisted.Status(), domain.SessionAwaitingRecovery),
	)
}

func sameConfirmedIntent(session domain.Session, intent ConfirmedSessionIntent) bool {
	return session.IntentID() == intent.IntentID &&
		session.ComputerID() == intent.ComputerID &&
		session.Provider() == intent.Provider &&
		session.Workdir() == intent.Workdir
}
