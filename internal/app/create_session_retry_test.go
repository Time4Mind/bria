package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

func TestSessionCreatorRetriesInitialStartWithSameDurableIdentity(t *testing.T) {
	t.Parallel()

	firstFailure := errors.New("first startup failure")
	secondFailure := errors.New("second startup failure")
	store := newMemorySessionStore()
	var requests []app.StartSessionRequest
	var observed []app.InitialStartAttemptFailure
	starter := &recordingStarter{start: func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
		requests = append(requests, request)
		switch len(requests) {
		case 1:
			return domain.ProviderBinding{}, firstFailure
		case 2:
			return domain.ProviderBinding{}, secondFailure
		default:
			return domain.ProviderBinding{
				Provider: request.Provider, SessionID: "provider-after-retry", Generation: 1,
			}, nil
		}
	}}
	creator, err := app.NewSessionCreator(
		"computer-1",
		allowingWorkdirValidator{},
		&sequenceIDs{ids: []domain.SessionID{"session-retry"}},
		store,
		starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 3, OnFailure: func(failure app.InitialStartAttemptFailure) {
			observed = append(observed, failure)
		}}),
	)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}

	result, err := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID: "intent-retry", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.StartError != nil || result.Session.Status() != domain.SessionReady {
		t.Fatalf("Create() result = (%q, %v), want ready without start error", result.Session.Status(), result.StartError)
	}
	if got, want := result.StartAttempts, 3; got != want {
		t.Fatalf("start attempts = %d, want %d", got, want)
	}
	if got, want := len(requests), 3; got != want {
		t.Fatalf("start requests = %d, want %d", got, want)
	}
	for index, request := range requests {
		if got, want := request.SessionID, domain.SessionID("session-retry"); got != want {
			t.Fatalf("request %d session = %q, want %q", index+1, got, want)
		}
	}
	if len(observed) != 2 || observed[0].SessionID != "session-retry" || observed[0].Attempt != 1 ||
		!errors.Is(observed[0].Err, firstFailure) || observed[1].Attempt != 2 || !errors.Is(observed[1].Err, secondFailure) {
		t.Fatalf("observed startup failures = %#v", observed)
	}
	if got, want := store.statuses(), []domain.SessionStatus{domain.SessionStarting, domain.SessionReady}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted statuses = %v, want %v", got, want)
	}
}

func TestSessionCreatorSignalsConfirmedEmptyDeletionAfterRetriesExhausted(t *testing.T) {
	t.Parallel()

	failures := []error{errors.New("startup one"), errors.New("startup two"), errors.New("startup three")}
	baseStore := newMemorySessionStore()
	store := &failedStartDeletingStore{memorySessionStore: baseStore, deleteEmpty: true}
	starter := &recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
		index := store.startCalls
		store.startCalls++
		if index >= len(failures) {
			t.Fatalf("unexpected startup attempt %d", index+1)
		}
		return domain.ProviderBinding{}, failures[index]
	}}
	creator, err := app.NewSessionCreator(
		"computer-1",
		allowingWorkdirValidator{},
		&sequenceIDs{ids: []domain.SessionID{"session-exhausted"}},
		store,
		starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 3}),
	)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}

	result, createErr := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID: "intent-exhausted", ComputerID: "computer-1", Provider: domain.ProviderClaude, Workdir: "/workspace/project",
	})
	if !errors.Is(createErr, app.ErrInitialStartExhausted) {
		t.Fatalf("Create() error = %v, want ErrInitialStartExhausted", createErr)
	}
	var exhausted *app.InitialStartExhaustedError
	if !errors.As(createErr, &exhausted) {
		t.Fatalf("Create() error type = %T, want *InitialStartExhaustedError", createErr)
	}
	if exhausted.Attempts != 3 || !exhausted.Result.Session.Equal(result.Session) {
		t.Fatalf("completion = (%d, %#v), want 3 attempts and returned failed result", exhausted.Attempts, exhausted.Result)
	}
	if got, want := result.StartAttempts, 3; got != want {
		t.Fatalf("result start attempts = %d, want %d", got, want)
	}
	if got, want := result.Session.ID(), domain.SessionID("session-exhausted"); got != want {
		t.Fatalf("failed result session = %q, want %q", got, want)
	}
	if result.Session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("failed result status = %q, want awaiting recovery snapshot", result.Session.Status())
	}
	for _, failure := range failures {
		if !errors.Is(result.StartError, failure) {
			t.Fatalf("start error = %v, want to retain %v", result.StartError, failure)
		}
	}
	if !store.deleted || store.deleteCalls != 1 {
		t.Fatalf("confirmed deletion = (%t, calls %d), want true and one call", store.deleted, store.deleteCalls)
	}
	if _, exists, loadErr := store.GetByIntent(context.Background(), "intent-exhausted"); loadErr != nil || exists {
		t.Fatalf("persisted intent after confirmed deletion = (exists %t, err %v), want absent", exists, loadErr)
	}
}

func TestSessionCreatorStopsRetryWhenDurableStartingSessionChanges(t *testing.T) {
	t.Parallel()

	startFailure := errors.New("startup failed")
	store := newMemorySessionStore()
	starter := &recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
		store.mu.Lock()
		defer store.mu.Unlock()
		for intent, current := range store.byIntent {
			changed, transitionErr := current.AwaitRecovery()
			if transitionErr != nil {
				t.Fatalf("AwaitRecovery() error = %v", transitionErr)
			}
			store.byIntent[intent] = changed
		}
		return domain.ProviderBinding{}, startFailure
	}}
	creator, err := app.NewSessionCreator(
		"computer-1",
		allowingWorkdirValidator{},
		&sequenceIDs{ids: []domain.SessionID{"session-changed"}},
		store,
		starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 3}),
	)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}

	result, createErr := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID: "intent-changed", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if !errors.Is(createErr, app.ErrOutcomeUnknown) {
		t.Fatalf("Create() error = %v, want ErrOutcomeUnknown for changed durable state", createErr)
	}
	if result.Session.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("returned current status = %q, want changed awaiting recovery", result.Session.Status())
	}
	if starter.calls != 1 {
		t.Fatalf("starter calls = %d, want one before durable identity changed", starter.calls)
	}
}

func TestSessionCreatorRetainsFailedStartWhenEmptyDeletionIsNotProven(t *testing.T) {
	t.Parallel()

	startFailure := errors.New("startup failed")
	baseStore := newMemorySessionStore()
	store := &failedStartDeletingStore{memorySessionStore: baseStore}
	starter := &recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
		return domain.ProviderBinding{}, startFailure
	}}
	creator, err := app.NewSessionCreator(
		"computer-1",
		allowingWorkdirValidator{},
		&sequenceIDs{ids: []domain.SessionID{"session-retained"}},
		store,
		starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 2}),
	)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}

	result, createErr := creator.Create(context.Background(), app.ConfirmedSessionIntent{
		IntentID: "intent-retained", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if createErr != nil {
		t.Fatalf("Create() error = %v, want retained durable failure result", createErr)
	}
	if result.Session.Status() != domain.SessionAwaitingRecovery || !errors.Is(result.StartError, startFailure) {
		t.Fatalf("Create() result = (%q, %v), want retained awaiting-recovery failure", result.Session.Status(), result.StartError)
	}
	if store.deleted || store.deleteCalls != 1 {
		t.Fatalf("deletion = (%t, calls %d), want false and one proof attempt", store.deleted, store.deleteCalls)
	}
	persisted, exists, loadErr := store.GetByIntent(context.Background(), "intent-retained")
	if loadErr != nil || !exists || !persisted.Equal(result.Session) {
		t.Fatalf("retained session = (exists %t, err %v, %#v), want exact failed result", exists, loadErr, persisted.Snapshot())
	}
}

func TestSessionCreatorCancellationAfterFailedStartDeletesEmptyHalfSession(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	baseStore := newMemorySessionStore()
	store := &failedStartDeletingStore{memorySessionStore: baseStore, deleteEmpty: true}
	starter := &recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
		cancel()
		return domain.ProviderBinding{}, errors.New("provider start failed before shutdown")
	}}
	creator, err := app.NewSessionCreator(
		"computer-1",
		allowingWorkdirValidator{},
		&sequenceIDs{ids: []domain.SessionID{"session-cancelled"}},
		store,
		starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 3, Delay: time.Second}),
	)
	if err != nil {
		t.Fatalf("NewSessionCreator() error = %v", err)
	}

	result, createErr := creator.Create(ctx, app.ConfirmedSessionIntent{
		IntentID: "intent-cancelled", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if !errors.Is(createErr, context.Canceled) {
		t.Fatalf("Create() error = %v, want context cancellation", createErr)
	}
	if result.StartAttempts != 1 || !store.deleted {
		t.Fatalf("cancelled result = attempts %d deleted %t, want one and deleted", result.StartAttempts, store.deleted)
	}
	if _, exists, loadErr := store.GetByIntent(context.Background(), "intent-cancelled"); loadErr != nil || exists {
		t.Fatalf("persisted cancelled half-session = (exists %t, err %v), want absent", exists, loadErr)
	}
}

func TestSessionCreatorPersistsSuccessfulHandshakeAfterCallerCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAwareSessionStore{memorySessionStore: newMemorySessionStore()}
	starter := &recordingStarter{start: func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
		cancel()
		return domain.ProviderBinding{Provider: request.Provider, SessionID: "provider-ready", Generation: 1}, nil
	}}
	creator, err := app.NewSessionCreator(
		"computer-1", allowingWorkdirValidator{}, &sequenceIDs{ids: []domain.SessionID{"session-ready-after-cancel"}}, store, starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, createErr := creator.Create(ctx, app.ConfirmedSessionIntent{
		IntentID: "intent-ready-after-cancel", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if createErr != nil || result.Session.Status() != domain.SessionReady || result.StartAttempts != 1 {
		t.Fatalf("Create() = status %q attempts %d error %v, want ready after one handshake", result.Session.Status(), result.StartAttempts, createErr)
	}
	if starter.abortCalls != 0 {
		t.Fatalf("successful persisted terminal was aborted %d times", starter.abortCalls)
	}
}

func TestSessionCreatorFencesCancellationThatRacesReadyPersistence(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelBeforeCASSessionStore{memorySessionStore: newMemorySessionStore(), cancel: cancel}
	starter := &recordingStarter{start: func(request app.StartSessionRequest) (domain.ProviderBinding, error) {
		return domain.ProviderBinding{Provider: request.Provider, SessionID: "provider-ready-race", Generation: 1}, nil
	}}
	creator, err := app.NewSessionCreator(
		"computer-1", allowingWorkdirValidator{}, &sequenceIDs{ids: []domain.SessionID{"session-ready-race"}}, store, starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 3}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, createErr := creator.Create(ctx, app.ConfirmedSessionIntent{
		IntentID: "intent-ready-race", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if createErr != nil || result.Session.Status() != domain.SessionReady || !store.cancelled {
		t.Fatalf("Create() = status %q cancelled %t error %v, want cancellation-fenced ready", result.Session.Status(), store.cancelled, createErr)
	}
}

func TestSessionCreatorFencesCancellationThatRacesFailedStartPersistence(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelBeforeCASSessionStore{memorySessionStore: newMemorySessionStore(), cancel: cancel}
	startFailure := errors.New("provider start failed")
	starter := &recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
		return domain.ProviderBinding{}, startFailure
	}}
	creator, err := app.NewSessionCreator(
		"computer-1", allowingWorkdirValidator{}, &sequenceIDs{ids: []domain.SessionID{"session-failed-race"}}, store, starter,
		app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: 1}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, createErr := creator.Create(ctx, app.ConfirmedSessionIntent{
		IntentID: "intent-failed-race", ComputerID: "computer-1", Provider: domain.ProviderCodex, Workdir: "/workspace/project",
	})
	if createErr != nil || result.Session.Status() != domain.SessionAwaitingRecovery || !errors.Is(result.StartError, startFailure) || !store.cancelled {
		t.Fatalf("Create() = status %q cancelled %t start error %v error %v", result.Session.Status(), store.cancelled, result.StartError, createErr)
	}
}

func TestInitialStartRetryPolicyRejectsUnboundedOrNegativeConfiguration(t *testing.T) {
	t.Parallel()

	for _, policy := range []app.InitialStartRetryPolicy{
		{},
		{MaxAttempts: -1},
		{MaxAttempts: 2, Delay: -1},
	} {
		_, err := app.NewSessionCreator(
			"computer-1",
			allowingWorkdirValidator{},
			&sequenceIDs{ids: []domain.SessionID{"unused"}},
			newMemorySessionStore(),
			&recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
				return domain.ProviderBinding{}, nil
			}},
			app.WithInitialStartRetry(policy),
		)
		if err == nil {
			t.Fatalf("NewSessionCreator(%+v) error = nil, want invalid retry policy", policy)
		}
	}
}

func TestInitialStartRetryPolicyEnforcesHardAttemptLimit(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		maxAttempts int
		wantErr     bool
	}{
		{name: "limit is accepted", maxAttempts: 5},
		{name: "above limit is rejected", maxAttempts: 6, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := app.NewSessionCreator(
				"computer-1",
				allowingWorkdirValidator{},
				&sequenceIDs{ids: []domain.SessionID{"unused"}},
				newMemorySessionStore(),
				&recordingStarter{start: func(app.StartSessionRequest) (domain.ProviderBinding, error) {
					return domain.ProviderBinding{}, nil
				}},
				app.WithInitialStartRetry(app.InitialStartRetryPolicy{MaxAttempts: test.maxAttempts}),
			)
			if (err != nil) != test.wantErr {
				t.Fatalf("NewSessionCreator(MaxAttempts: %d) error = %v, wantErr %t", test.maxAttempts, err, test.wantErr)
			}
		})
	}
}

type failedStartDeletingStore struct {
	*memorySessionStore
	deleteEmpty bool
	deleteCalls int
	startCalls  int
	deleted     bool
}

type cancelAwareSessionStore struct{ *memorySessionStore }

type cancelBeforeCASSessionStore struct {
	*memorySessionStore
	cancel    context.CancelFunc
	cancelled bool
}

func (store *cancelBeforeCASSessionStore) CompareAndSwap(ctx context.Context, expected, next domain.Session) error {
	store.cancel()
	store.cancelled = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("ready persistence context is not bounded")
	}
	return store.memorySessionStore.CompareAndSwap(ctx, expected, next)
}

func (store *cancelAwareSessionStore) CompareAndSwap(ctx context.Context, expected, next domain.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.memorySessionStore.CompareAndSwap(ctx, expected, next)
}

func (store *cancelAwareSessionStore) Load(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	return store.memorySessionStore.Load(ctx, id)
}

func (store *failedStartDeletingStore) DeleteEmptyFailedStart(_ context.Context, expected domain.Session) (bool, error) {
	store.deleteCalls++
	target, recovering := expected.RecoveryTarget()
	_, bound := expected.Binding()
	if expected.Status() != domain.SessionAwaitingRecovery || !recovering || target != domain.SessionStarting || bound {
		return false, errors.New("delete called without an unbound initial-start failure")
	}
	if !store.deleteEmpty {
		return false, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.byIntent[expected.IntentID()]
	if !exists || !current.Equal(expected) {
		return false, errors.New("failed-start session changed")
	}
	delete(store.byIntent, expected.IntentID())
	store.deleted = true
	return true, nil
}
