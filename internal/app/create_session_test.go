package app_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

func TestSessionCreatorPersistsStartingBeforeStartAndThenReady(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newMemorySessionStore()
	binding := domain.ProviderBinding{
		Provider:   domain.ProviderCodex,
		SessionID:  "provider-session-42",
		Generation: 7,
	}
	starter := &recordingStarter{
		start: func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
			if request.Mode != app.SessionStartNew || request.PriorBinding != nil {
				t.Fatalf("start request lifecycle = (%q, %+v), want new without prior binding", request.Mode, request.PriorBinding)
			}
			stored, ok := store.byIntent[domain.IntentID("intent-1")]
			if !ok || stored.Status() != domain.SessionStarting {
				t.Fatalf("starter observed store = (%v, %v), want durable starting session", stored.Snapshot(), ok)
			}
			if got, want := request, (app.StartSessionRequest{
				SessionID:  "session-1",
				ComputerID: "computer-1",
				Provider:   domain.ProviderCodex,
				Workdir:    "/workspace/project",
				Mode:       app.SessionStartNew,
			}); got != want {
				t.Fatalf("start request = %#v, want %#v", got, want)
			}
			return binding, nil
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{"session-1"}}, store, starter)

	result, err := creator.Create(ctx, app.ConfirmedSessionIntent{
		IntentID:   "intent-1",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.StartError != nil {
		t.Fatalf("Create() start error = %v", result.StartError)
	}
	if result.Replayed {
		t.Fatal("Create() replayed = true, want false")
	}
	if got, want := result.Session.Status(), domain.SessionReady; got != want {
		t.Fatalf("session status = %q, want %q", got, want)
	}
	if got, ok := result.Session.Binding(); !ok || got != binding {
		t.Fatalf("session binding = (%#v, %v), want (%#v, true)", got, ok, binding)
	}
	if got, want := store.statuses(), []domain.SessionStatus{
		domain.SessionStarting,
		domain.SessionReady,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted statuses = %v, want %v", got, want)
	}
}

func TestStartSessionRequestRequiresExplicitConsistentLifecycleMode(t *testing.T) {
	t.Parallel()

	base := app.StartSessionRequest{
		SessionID: "logical-1", ComputerID: "computer-1", Provider: domain.ProviderCodex,
		Workdir: "/workspace/project",
	}
	tests := []struct {
		name    string
		request app.StartSessionRequest
		wantErr bool
	}{
		{name: "new", request: withStartMode(base, app.SessionStartNew, nil)},
		{name: "new cannot carry prior binding", request: withStartMode(base, app.SessionStartNew, &domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}), wantErr: true},
		{name: "resume requires prior binding", request: withStartMode(base, app.SessionStartResume, nil), wantErr: true},
		{name: "resume provider must match", request: withStartMode(base, app.SessionStartResume, &domain.ProviderBinding{Provider: domain.ProviderClaude, SessionID: "session", Generation: 1}), wantErr: true},
		{name: "resume", request: withStartMode(base, app.SessionStartResume, &domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1})},
		{name: "mode is explicit", request: base, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestSessionCreatorPersistsConfiguredLifetimeFromCreation(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 9, 2, 14, 0, 0, 0, time.UTC)
	store := newMemorySessionStore()
	starter := &recordingStarter{start: func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
		return domain.ProviderBinding{Provider: request.Provider, SessionID: "provider-session", Generation: 1}, nil
	}}
	creator, err := app.NewSessionCreator(
		"computer-1", allowingWorkdirValidator{}, &sequenceIDs{ids: []domain.SessionID{"session-lifetime"}}, store, starter,
		app.WithSessionLifetime(domain.SessionLifetime24Hours),
		app.WithSessionClock(func() time.Time { return createdAt }),
	)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}
	result, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID: "intent-lifetime", ComputerID: "computer-1", Provider: domain.ProviderClaude, Workdir: "/workspace/project",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if got, want := result.Session.CreatedAt(), createdAt; !got.Equal(want) {
		t.Fatalf("created at = %s, want %s", got, want)
	}
	if got, ok := result.Session.Deadline(); !ok || !got.Equal(createdAt.Add(24*time.Hour)) {
		t.Fatalf("deadline = (%s, %t), want %s", got, ok, createdAt.Add(24*time.Hour))
	}
}

func withStartMode(request app.StartSessionRequest, mode app.SessionStartMode, prior *domain.ProviderBinding) app.StartSessionRequest {
	request.Mode = mode
	request.PriorBinding = prior
	return request
}

func TestSessionCreatorPersistsAwaitingRecoveryOnStartFailure(t *testing.T) {
	t.Parallel()

	startFailure := errors.New("child process exited during startup")
	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			return domain.ProviderBinding{}, startFailure
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{"session-2"}}, store, starter)

	result, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID:   "intent-2",
		ComputerID: "computer-1",
		Provider:   domain.ProviderClaude,
		Workdir:    "/workspace/other",
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want durable start failure in result", err)
	}
	if !errors.Is(result.StartError, startFailure) {
		t.Fatalf("Create() start error = %v, want %v", result.StartError, startFailure)
	}
	if got, want := result.Session.Status(), domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("session status = %q, want %q", got, want)
	}
	if _, ok := result.Session.Binding(); ok {
		t.Fatal("awaiting-recovery session unexpectedly has a provider binding")
	}
	if got, want := store.statuses(), []domain.SessionStatus{
		domain.SessionStarting,
		domain.SessionAwaitingRecovery,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted statuses = %v, want %v", got, want)
	}
}

func TestSessionCreatorReplaysIntentWithoutStartingAnotherProcess(t *testing.T) {
	t.Parallel()

	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			return domain.ProviderBinding{
				Provider:   domain.ProviderCodex,
				SessionID:  "provider-session-replayed",
				Generation: 1,
			}, nil
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{
		"session-3",
		"unused-session",
	}}, store, starter)
	intent := app.ConfirmedSessionIntent{
		IntentID:   "intent-3",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	}

	first, err := creator.Create(context.Background(), intent)
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	second, err := creator.Create(context.Background(), intent)
	if err != nil {
		t.Fatalf("replayed Create() error = %v", err)
	}
	if !second.Replayed {
		t.Fatal("replayed Create() replayed = false, want true")
	}
	if second.Session.ID() != first.Session.ID() {
		t.Fatalf("replayed session id = %q, want %q", second.Session.ID(), first.Session.ID())
	}
	if got, want := starter.calls, 1; got != want {
		t.Fatalf("starter calls = %d, want %d", got, want)
	}
}

func TestSessionCreatorConcurrentReplayStartsOneProcess(t *testing.T) {
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			close(startEntered)
			<-releaseStart
			return domain.ProviderBinding{
				Provider:   domain.ProviderCodex,
				SessionID:  "provider-session-concurrent",
				Generation: 1,
			}, nil
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{
		"session-concurrent",
		"unused-concurrent-session",
	}}, store, starter)
	intent := app.ConfirmedSessionIntent{
		IntentID:   "intent-concurrent",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := creator.Create(context.Background(), intent)
		firstDone <- err
	}()
	<-startEntered

	replayed, err := creator.Create(context.Background(), intent)
	if err != nil {
		t.Fatalf("concurrent replay Create() error = %v", err)
	}
	if !replayed.Replayed {
		t.Fatal("concurrent replay replayed = false, want true")
	}
	if got, want := replayed.Session.ID(), domain.SessionID("session-concurrent"); got != want {
		t.Fatalf("concurrent replay session id = %q, want %q", got, want)
	}
	close(releaseStart)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if got, want := starter.calls, 1; got != want {
		t.Fatalf("starter calls = %d, want %d", got, want)
	}
}

func TestSessionCreatorDoesNotStartWhenInitialPersistenceFails(t *testing.T) {
	t.Parallel()

	persistFailure := errors.New("disk unavailable")
	store := newMemorySessionStore()
	store.putErr = persistFailure
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			t.Fatal("Start() called after initial persistence failed")
			return domain.ProviderBinding{}, nil
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{"session-4"}}, store, starter)

	_, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID:   "intent-4",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	})
	if !errors.Is(err, persistFailure) {
		t.Fatalf("Create() error = %v, want persistence error %v", err, persistFailure)
	}
	if got := starter.calls; got != 0 {
		t.Fatalf("starter calls = %d, want 0", got)
	}
}

func TestSessionCreatorValidatesBeforeAllocatingOrWriting(t *testing.T) {
	t.Parallel()

	ids := &sequenceIDs{ids: []domain.SessionID{"must-not-be-used"}}
	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			t.Fatal("Start() called for invalid intent")
			return domain.ProviderBinding{}, nil
		},
	}
	creator := mustSessionCreator(t, ids, store, starter)

	_, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want validation error")
	}
	if got := ids.calls; got != 0 {
		t.Fatalf("id source calls = %d, want 0", got)
	}
	if got := store.putCalls; got != 0 {
		t.Fatalf("store put calls = %d, want 0", got)
	}
	if got := starter.calls; got != 0 {
		t.Fatalf("starter calls = %d, want 0", got)
	}
}

func TestSessionCreatorRejectsInvalidWorkdirBeforeAllocatingWritingOrStarting(t *testing.T) {
	t.Parallel()

	validationError := errors.New("confirmed workdir is unusable")
	validator := &recordingWorkdirValidator{err: validationError}
	ids := &sequenceIDs{ids: []domain.SessionID{"must-not-be-used"}}
	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			t.Fatal("Start() called for invalid workdir")
			return domain.ProviderBinding{}, nil
		},
	}
	creator := mustSessionCreatorWithWorkdirs(t, validator, ids, store, starter)

	_, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID:   "intent-invalid-workdir",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/missing",
	})
	if !errors.Is(err, validationError) {
		t.Fatalf("Create() error = %v, want workdir validation error %v", err, validationError)
	}
	if validator.calls != 1 {
		t.Fatalf("workdir validator calls = %d, want 1", validator.calls)
	}
	if ids.calls != 0 || store.putCalls != 0 || starter.calls != 0 {
		t.Fatalf(
			"downstream calls after invalid workdir: ids=%d put=%d start=%d, want all zero",
			ids.calls,
			store.putCalls,
			starter.calls,
		)
	}
}

func TestSessionCreatorReplayAndConflictSkipWorkdirValidationAndSideEffects(t *testing.T) {
	t.Parallel()

	stored, err := domain.NewStartingSession(
		"session-existing",
		"intent-existing",
		"computer-1",
		domain.ProviderCodex,
		"/workspace/original",
	)
	if err != nil {
		t.Fatalf("NewStartingSession() error = %v", err)
	}

	tests := []struct {
		name      string
		intent    app.ConfirmedSessionIntent
		conflicts bool
	}{
		{
			name: "exact replay",
			intent: app.ConfirmedSessionIntent{
				IntentID:   stored.IntentID(),
				ComputerID: stored.ComputerID(),
				Provider:   stored.Provider(),
				Workdir:    stored.Workdir(),
			},
		},
		{
			name: "immutable conflict",
			intent: app.ConfirmedSessionIntent{
				IntentID:   stored.IntentID(),
				ComputerID: stored.ComputerID(),
				Provider:   stored.Provider(),
				Workdir:    "/workspace/changed",
			},
			conflicts: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := &recordingWorkdirValidator{err: errors.New("validator must not run")}
			ids := &sequenceIDs{ids: []domain.SessionID{"must-not-be-used"}}
			store := newMemorySessionStore()
			store.byIntent[stored.IntentID()] = stored
			starter := &recordingStarter{
				start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
					t.Fatal("Start() called for persisted intent")
					return domain.ProviderBinding{}, nil
				},
			}
			creator := mustSessionCreatorWithWorkdirs(t, validator, ids, store, starter)

			result, err := creator.Create(context.Background(), test.intent)
			if test.conflicts {
				if !errors.Is(err, app.ErrIntentConflict) {
					t.Fatalf("Create() error = %v, want ErrIntentConflict", err)
				}
			} else {
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				if !result.Replayed || !result.Session.Equal(stored) {
					t.Fatalf("Create() result = %#v, want exact replay", result)
				}
			}
			if validator.calls != 0 || ids.calls != 0 || store.putCalls != 0 || starter.calls != 0 {
				t.Fatalf(
					"calls for persisted intent: validate=%d ids=%d put=%d start=%d, want all zero",
					validator.calls,
					ids.calls,
					store.putCalls,
					starter.calls,
				)
			}
		})
	}
}

func TestSessionCreatorAbortsInvalidSuccessfulBinding(t *testing.T) {
	t.Parallel()

	invalidBinding := domain.ProviderBinding{
		Provider:  domain.ProviderClaude,
		SessionID: "provider-session-invalid",
	}
	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			return invalidBinding, nil
		},
		abort: func(_ app.StartSessionRequest, binding domain.ProviderBinding) error {
			if binding != invalidBinding {
				t.Fatalf("Abort() binding = %#v, want %#v", binding, invalidBinding)
			}
			return nil
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{"session-5"}}, store, starter)

	result, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID:   "intent-5",
		ComputerID: "computer-1",
		Provider:   domain.ProviderClaude,
		Workdir:    "/workspace/project",
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want confirmed awaiting-recovery result", err)
	}
	if result.StartError == nil {
		t.Fatal("Create() start error = nil, want invalid-binding failure")
	}
	if got, want := result.Session.Status(), domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("session status = %q, want %q", got, want)
	}
	if got, want := starter.abortCalls, 1; got != want {
		t.Fatalf("abort calls = %d, want %d", got, want)
	}
}

func TestSessionCreatorReportsUnknownOutcomeWhenInvalidStartCannotBeAborted(t *testing.T) {
	t.Parallel()

	abortFailure := errors.New("child still running")
	store := newMemorySessionStore()
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			return domain.ProviderBinding{
				Provider:  domain.ProviderCodex,
				SessionID: "provider-session-invalid",
			}, nil
		},
		abort: func(app.StartSessionRequest, domain.ProviderBinding) error {
			return abortFailure
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{"session-6"}}, store, starter)

	result, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID:   "intent-6",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	})
	if !errors.Is(err, app.ErrOutcomeUnknown) || !errors.Is(err, abortFailure) {
		t.Fatalf("Create() error = %v, want outcome-unknown and abort errors", err)
	}
	if got, want := result.Session.Status(), domain.SessionStarting; got != want {
		t.Fatalf("session status = %q, want %q", got, want)
	}
	if got, want := store.statuses(), []domain.SessionStatus{domain.SessionStarting}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted statuses = %v, want %v", got, want)
	}
}

func TestSessionCreatorAbortsWhenReadyCannotBePersisted(t *testing.T) {
	t.Parallel()

	readyFailure := errors.New("ready compare-and-swap failed")
	store := newMemorySessionStore()
	store.casErrors = []error{readyFailure, nil}
	starter := &recordingStarter{
		start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
			return domain.ProviderBinding{
				Provider:   domain.ProviderCodex,
				SessionID:  "provider-session-unpersisted",
				Generation: 1,
			}, nil
		},
	}
	creator := mustSessionCreator(t, &sequenceIDs{ids: []domain.SessionID{"session-7"}}, store, starter)

	result, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID:   "intent-7",
		ComputerID: "computer-1",
		Provider:   domain.ProviderCodex,
		Workdir:    "/workspace/project",
	})
	if err != nil {
		t.Fatalf("Create() error = %v, want confirmed awaiting-recovery result", err)
	}
	if !errors.Is(result.StartError, readyFailure) {
		t.Fatalf("Create() start error = %v, want %v", result.StartError, readyFailure)
	}
	if got, want := result.Session.Status(), domain.SessionAwaitingRecovery; got != want {
		t.Fatalf("session status = %q, want %q", got, want)
	}
	if got, want := starter.abortCalls, 1; got != want {
		t.Fatalf("abort calls = %d, want %d", got, want)
	}
}

type sequenceIDs struct {
	mu    sync.Mutex
	ids   []domain.SessionID
	calls int
}

func (source *sequenceIDs) NewSessionID(context.Context) (domain.SessionID, error) {
	source.mu.Lock()
	defer source.mu.Unlock()

	source.calls++
	id := source.ids[0]
	source.ids = source.ids[1:]
	return id, nil
}

type memorySessionStore struct {
	mu        sync.Mutex
	byIntent  map[domain.IntentID]domain.Session
	events    []domain.SessionSnapshot
	putCalls  int
	putErr    error
	casErrors []error
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{byIntent: make(map[domain.IntentID]domain.Session)}
}

func (store *memorySessionStore) PutStartingIfAbsent(
	_ context.Context,
	session domain.Session,
) (domain.Session, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.putCalls++
	if store.putErr != nil {
		return domain.Session{}, false, store.putErr
	}

	if existing, ok := store.byIntent[session.IntentID()]; ok {
		return existing, false, nil
	}
	store.byIntent[session.IntentID()] = session
	store.events = append(store.events, session.Snapshot())
	return session, true, nil
}

func (store *memorySessionStore) GetByIntent(
	_ context.Context,
	intentID domain.IntentID,
) (domain.Session, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.byIntent[intentID]
	return session, ok, nil
}

func (store *memorySessionStore) CompareAndSwap(
	_ context.Context,
	expected domain.Session,
	next domain.Session,
) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.casErrors) > 0 {
		err := store.casErrors[0]
		store.casErrors = store.casErrors[1:]
		if err != nil {
			return err
		}
	}

	current, ok := store.byIntent[expected.IntentID()]
	if !ok || !reflect.DeepEqual(current.Snapshot(), expected.Snapshot()) {
		return errors.New("compare and swap conflict")
	}
	store.byIntent[next.IntentID()] = next
	store.events = append(store.events, next.Snapshot())
	return nil
}

func (store *memorySessionStore) Load(
	_ context.Context,
	id domain.SessionID,
) (domain.Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	for _, session := range store.byIntent {
		if session.ID() == id {
			return session, nil
		}
	}
	return domain.Session{}, errors.New("session not found")
}

func (store *memorySessionStore) statuses() []domain.SessionStatus {
	store.mu.Lock()
	defer store.mu.Unlock()

	statuses := make([]domain.SessionStatus, 0, len(store.events))
	for _, event := range store.events {
		statuses = append(statuses, event.Status)
	}
	return statuses
}

type recordingStarter struct {
	start      func(app.StartSessionRequest) (domain.ProviderBinding, error)
	abort      func(app.StartSessionRequest, domain.ProviderBinding) error
	calls      int
	abortCalls int
}

type allowingWorkdirValidator struct{}

func (allowingWorkdirValidator) Validate(context.Context, string) error { return nil }

type recordingWorkdirValidator struct {
	calls int
	err   error
}

func (validator *recordingWorkdirValidator) Validate(context.Context, string) error {
	validator.calls++
	return validator.err
}

func (starter *recordingStarter) Start(
	_ context.Context,
	request app.StartSessionRequest,
) (domain.ProviderBinding, error) {
	starter.calls++
	return starter.start(request)
}

func (starter *recordingStarter) Abort(
	_ context.Context,
	request app.StartSessionRequest,
	binding domain.ProviderBinding,
) error {
	starter.abortCalls++
	if starter.abort == nil {
		return nil
	}
	return starter.abort(request, binding)
}

func mustSessionCreator(
	t *testing.T,
	ids app.SessionIDSource,
	store app.SessionStore,
	starter app.SessionStarter,
) *app.SessionCreator {
	t.Helper()
	return mustSessionCreatorWithWorkdirs(t, allowingWorkdirValidator{}, ids, store, starter)
}

func mustSessionCreatorWithWorkdirs(
	t *testing.T,
	workdirs app.WorkdirValidator,
	ids app.SessionIDSource,
	store app.SessionStore,
	starter app.SessionStarter,
) *app.SessionCreator {
	t.Helper()
	creator, err := app.NewSessionCreator("computer-1", workdirs, ids, store, starter)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}
	return creator
}
