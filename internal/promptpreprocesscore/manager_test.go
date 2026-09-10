package promptpreprocesscore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
)

type memoryBindingStore struct {
	mu      sync.Mutex
	records map[BindingKey]Binding
}

func newMemoryBindingStore() *memoryBindingStore {
	return &memoryBindingStore{records: make(map[BindingKey]Binding)}
}

func (store *memoryBindingStore) Load(_ context.Context, key BindingKey) (Binding, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[key]
	return record, found, nil
}

func (store *memoryBindingStore) Save(_ context.Context, binding Binding) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records[binding.Key] = binding
	return nil
}

func (store *memoryBindingStore) List(context.Context) ([]Binding, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	result := make([]Binding, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result, nil
}

func (store *memoryBindingStore) Delete(_ context.Context, key BindingKey) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.records, key)
	return nil
}

func (store *memoryBindingStore) binding(t *testing.T, key BindingKey) Binding {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	binding, found := store.records[key]
	if !found {
		t.Fatalf("binding %v was not persisted", key)
	}
	return binding
}

type managerFixtureSession struct {
	threadID     string
	started      chan<- string
	release      <-chan struct{}
	closed       chan<- string
	closeStarted chan<- struct{}
	closeRelease <-chan struct{}
	closeErr     error

	mu    sync.Mutex
	turns int
}

func (session *managerFixtureSession) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	session.mu.Lock()
	session.turns++
	turn := session.turns
	session.mu.Unlock()
	session.started <- fmt.Sprintf("%s:%s:%d", session.threadID, request.MessageID, turn)
	select {
	case <-ctx.Done():
		return promptpreprocess.Result{}, ctx.Err()
	case <-session.release:
		return promptpreprocess.Result{Text: fmt.Sprintf("result-%s-%d", session.threadID, turn)}, nil
	}
}

func (session *managerFixtureSession) Binding() string { return session.threadID }

func (session *managerFixtureSession) Close(ctx context.Context) error {
	if session.closed != nil {
		session.closed <- session.threadID
	}
	if session.closeStarted != nil {
		session.closeStarted <- struct{}{}
	}
	if session.closeRelease != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-session.closeRelease:
		}
	}
	return session.closeErr
}

func satelliteRequest(mode Mode, sessionID, messageID string, sequence uint64) Request {
	return Request{Mode: mode,
		ComputerID: "local", SessionID: domain.SessionID(sessionID), MessageID: messageID,
		Sequence: sequence, Instruction: "clean", Text: "raw " + messageID,
	}
}

func TestManagerSharedUsesOnePersistentSessionAndStrictAcceptanceFIFO(t *testing.T) {
	started, closed := make(chan string, 8), make(chan string, 8)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var mu sync.Mutex
	starts := 0
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		mu.Lock()
		defer mu.Unlock()
		starts++
		return &managerFixtureSession{threadID: "shared-thread", started: started, release: releases[starts-1], closed: closed}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModeShared); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result promptpreprocess.Result
		err    error
	}
	firstDone, secondDone := make(chan outcome, 1), make(chan outcome, 1)
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModeShared, "session-a", "first", 1))
		firstDone <- outcome{result: result, err: err}
	}()
	if got := receiveString(t, started); got != "shared-thread:first:1" {
		t.Fatalf("first turn = %q", got)
	}
	close(releases[0])
	first := <-firstDone
	if first.err != nil || first.result.Completion == nil {
		t.Fatalf("first = %#v, %v", first.result, first.err)
	}
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModeShared, "session-b", "second", 1))
		secondDone <- outcome{result: result, err: err}
	}()
	select {
	case got := <-started:
		t.Fatalf("second turn %q started before durable acceptance", got)
	case <-time.After(30 * time.Millisecond):
	}
	if err := first.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := receiveString(t, started); got != "shared-thread:second:2" {
		t.Fatalf("second turn = %q", got)
	}
	close(releases[1])
	second := <-secondDone
	if second.err != nil || second.result.Completion == nil {
		t.Fatalf("second = %#v, %v", second.result, second.err)
	}
	if err := second.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts != 1 {
		t.Fatalf("provider starts = %d, want one persistent shared satellite", starts)
	}
	select {
	case thread := <-closed:
		t.Fatalf("accepted request closed persistent satellite %q", thread)
	default:
	}
}

func TestManagerReplaysDuplicateUnacceptedRequestWithoutSecondTurn(t *testing.T) {
	started := make(chan string, 4)
	release := make(chan struct{})
	close(release)
	manager := newManager(func(context.Context, StartRequest) (Session, error) {
		return &managerFixtureSession{threadID: "shared-thread", started: started, release: release}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModeShared); err != nil {
		t.Fatal(err)
	}
	request := satelliteRequest(ModeShared, "session-a", "same", 1)
	first, err := manager.Process(context.Background(), request)
	if err != nil || first.Completion == nil || receiveString(t, started) != "shared-thread:same:1" {
		t.Fatalf("first = %#v, %v", first, err)
	}
	second, err := manager.Process(context.Background(), request)
	if err != nil || second.Text != first.Text || second.Completion == nil {
		t.Fatalf("replay = %#v, %v", second, err)
	}
	select {
	case turn := <-started:
		t.Fatalf("duplicate reached provider as %q", turn)
	default:
	}
	if err := second.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPerSessionSatellitesArePersistentAndParallel(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	var mu sync.Mutex
	starts := map[BindingKey]int{}
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		mu.Lock()
		starts[request.Key]++
		mu.Unlock()
		return &managerFixtureSession{threadID: "thread-" + string(request.Key.SessionID), started: started, release: release}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.SessionID{"session-a", "session-b"} {
		if err := manager.Activate(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan promptpreprocess.Result, 2)
	for _, id := range []string{"session-a", "session-b"} {
		go func(id string) {
			result, err := manager.Process(context.Background(), satelliteRequest(ModePerSession, id, "first", 1))
			if err != nil {
				t.Errorf("process %s: %v", id, err)
			}
			done <- result
		}(id)
	}
	seen := map[string]bool{receiveString(t, started): true, receiveString(t, started): true}
	if !seen["thread-session-a:first:1"] || !seen["thread-session-b:first:1"] {
		t.Fatalf("parallel starts = %#v", seen)
	}
	close(release)
	for range 2 {
		result := <-done
		if result.Completion == nil {
			t.Fatal("missing completion")
		}
		if err := result.Completion.Accept(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if starts[BindingKey{Mode: ModePerSession, SessionID: "session-a"}] != 1 || starts[BindingKey{Mode: ModePerSession, SessionID: "session-b"}] != 1 {
		t.Fatalf("starts = %#v", starts)
	}
}

func TestManagerArchiveAndRestoreResumesSameBinding(t *testing.T) {
	store := newMemoryBindingStore()
	started, closed := make(chan string, 4), make(chan string, 4)
	startRequests := make(chan StartRequest, 4)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		startRequests <- request
		threadID := request.ResumeProviderSessionID
		if threadID == "" {
			threadID = "thread-persisted"
		}
		return &managerFixtureSession{threadID: threadID, started: started, release: make(chan struct{}), closed: closed}, nil
	}, store)
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	id := domain.SessionID("session-a")
	if err := manager.Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	firstStart := receiveStart(t, startRequests)
	if firstStart.ResumeProviderSessionID != "" {
		t.Fatalf("new satellite unexpectedly resumed %q", firstStart.ResumeProviderSessionID)
	}
	key := BindingKey{Mode: ModePerSession, SessionID: id}
	waitBinding(t, store, key, DesiredActive, "thread-persisted")
	if err := manager.Archive(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if receiveString(t, closed) != "thread-persisted" {
		t.Fatal("archive did not close provider process")
	}
	waitBinding(t, store, key, DesiredArchived, "thread-persisted")
	if err := manager.Restore(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	resumed := receiveStart(t, startRequests)
	if resumed.ResumeProviderSessionID != "thread-persisted" {
		t.Fatalf("restore resumed %q", resumed.ResumeProviderSessionID)
	}
	waitBinding(t, store, key, DesiredActive, "thread-persisted")
}

func TestManagerRestartReconnectsSharedSatelliteThread(t *testing.T) {
	store := newMemoryBindingStore()
	firstStarts := make(chan StartRequest, 1)
	first := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		firstStarts <- request
		return &managerFixtureSession{threadID: "shared-restart-thread", started: make(chan string, 1), release: make(chan struct{})}, nil
	}, store)
	if err := first.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if request := receiveStart(t, firstStarts); request.ResumeProviderSessionID != "" || request.Key != (BindingKey{Mode: ModeShared}) {
		t.Fatalf("first shared start = %#v", request)
	}
	waitBinding(t, store, BindingKey{Mode: ModeShared}, DesiredActive, "shared-restart-thread")
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	restarted := make(chan StartRequest, 1)
	second := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		restarted <- request
		return &managerFixtureSession{threadID: request.ResumeProviderSessionID, started: make(chan string, 1), release: make(chan struct{})}, nil
	}, store)
	defer second.Close(context.Background())
	if err := second.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if request := receiveStart(t, restarted); request.Key != (BindingKey{Mode: ModeShared}) || request.ResumeProviderSessionID != "shared-restart-thread" {
		t.Fatalf("shared restart did not reconnect exact thread: %#v", request)
	}
}

func TestManagerReplacesOnlyProvenMissingEmptySharedThread(t *testing.T) {
	store := newMemoryBindingStore()
	key := BindingKey{Mode: ModeShared}
	if err := store.Save(context.Background(), Binding{Key: key, Desired: DesiredActive, ProviderSessionID: "empty-thread"}); err != nil {
		t.Fatal(err)
	}
	starts := make(chan StartRequest, 2)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		if request.ResumeProviderSessionID != "" {
			return nil, ErrResumeUnavailable
		}
		return &managerFixtureSession{threadID: "replacement-thread", started: make(chan string, 1), release: make(chan struct{})}, nil
	}, store)
	defer manager.Close(context.Background())
	if err := manager.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if first := receiveStart(t, starts); first.ResumeProviderSessionID != "empty-thread" {
		t.Fatalf("first start = %#v, want exact resume", first)
	}
	if replacement := receiveStart(t, starts); replacement.ResumeProviderSessionID != "" {
		t.Fatalf("replacement start = %#v, want fresh thread", replacement)
	}
	waitBinding(t, store, key, DesiredActive, "replacement-thread")
}

func TestManagerDoesNotReplaceThreadAfterUnclassifiedResumeFailure(t *testing.T) {
	store := newMemoryBindingStore()
	key := BindingKey{Mode: ModeShared}
	if err := store.Save(context.Background(), Binding{Key: key, Desired: DesiredActive, ProviderSessionID: "existing-thread"}); err != nil {
		t.Fatal(err)
	}
	starts := make(chan StartRequest, 2)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		return nil, ErrInvocation
	}, store)
	defer manager.Close(context.Background())
	if err := manager.Warmup(context.Background()); !errors.Is(err, ErrInvocation) {
		t.Fatalf("Warmup() error = %v, want ErrInvocation", err)
	}
	if first := receiveStart(t, starts); first.ResumeProviderSessionID != "existing-thread" {
		t.Fatalf("first start = %#v, want exact resume", first)
	}
	select {
	case extra := <-starts:
		t.Fatalf("unclassified failure created replacement: %#v", extra)
	case <-time.After(20 * time.Millisecond):
	}
	if got := store.binding(t, key).ProviderSessionID; got != "existing-thread" {
		t.Fatalf("binding = %q, want preserved existing thread", got)
	}
}

func TestManagerRestartReconnectsOnlyActivePerSessionSatellites(t *testing.T) {
	store := newMemoryBindingStore()
	activeKey := BindingKey{Mode: ModePerSession, SessionID: "main-active"}
	archivedKey := BindingKey{Mode: ModePerSession, SessionID: "main-archived"}
	if err := store.Save(context.Background(), Binding{Key: activeKey, Desired: DesiredActive, ProviderSessionID: "active-thread"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), Binding{Key: archivedKey, Desired: DesiredArchived, ProviderSessionID: "archived-thread"}); err != nil {
		t.Fatal(err)
	}
	starts := make(chan StartRequest, 2)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		return &managerFixtureSession{threadID: request.ResumeProviderSessionID, started: make(chan string, 1), release: make(chan struct{})}, nil
	}, store)
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), []PrimaryState{
		{SessionID: activeKey.SessionID, Desired: DesiredActive},
		{SessionID: archivedKey.SessionID, Desired: DesiredArchived},
	}); err != nil {
		t.Fatal(err)
	}
	if request := receiveStart(t, starts); request.Key != activeKey || request.ResumeProviderSessionID != "active-thread" {
		t.Fatalf("per-session restart did not reconnect exact active thread: %#v", request)
	}
	select {
	case request := <-starts:
		t.Fatalf("archived per-session satellite restarted: %#v", request)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestManagerForgetClosesSatelliteAndDeletesHiddenBinding(t *testing.T) {
	store := newMemoryBindingStore()
	starts, closed := make(chan StartRequest, 2), make(chan string, 2)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		return &managerFixtureSession{threadID: "thread-empty", started: make(chan string, 1), release: make(chan struct{}), closed: closed}, nil
	}, store)
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	id := domain.SessionID("empty-main")
	if err := manager.Activate(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	receiveStart(t, starts)
	waitBinding(t, store, BindingKey{Mode: ModePerSession, SessionID: id}, DesiredActive, "thread-empty")
	if err := manager.Forget(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if receiveString(t, closed) != "thread-empty" {
		t.Fatal("forgotten satellite process was not closed")
	}
	if _, found, err := store.Load(context.Background(), BindingKey{Mode: ModePerSession, SessionID: id}); err != nil || found {
		t.Fatalf("forgotten binding remains: found=%t err=%v", found, err)
	}
}

func TestManagerLifecycleReturnsStopFailureAfterDurableStateChange(t *testing.T) {
	stopErr := errors.New("satellite stop failed")
	for _, operation := range []string{"archive", "forget"} {
		t.Run(operation, func(t *testing.T) {
			store := newMemoryBindingStore()
			manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
				return &managerFixtureSession{
					threadID: "thread-stop", started: make(chan string, 1), release: make(chan struct{}), closeErr: stopErr,
				}, nil
			}, store)
			defer manager.Close(context.Background())
			if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
				t.Fatal(err)
			}
			id := domain.SessionID("main-stop")
			if err := manager.Activate(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			key := BindingKey{Mode: ModePerSession, SessionID: id}
			waitBinding(t, store, key, DesiredActive, "thread-stop")
			var err error
			if operation == "archive" {
				err = manager.Archive(context.Background(), id)
			} else {
				err = manager.Forget(context.Background(), id)
			}
			if !errors.Is(err, stopErr) {
				t.Fatalf("lifecycle error = %v, want stop failure", err)
			}
			binding, found, loadErr := store.Load(context.Background(), key)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if operation == "archive" {
				if !found || binding.Desired != DesiredArchived || binding.ProviderSessionID != "thread-stop" {
					t.Fatalf("archive binding = %#v, found=%t", binding, found)
				}
			} else if found {
				t.Fatalf("forget retained binding %#v", binding)
			}
		})
	}
}

func TestManagerRemembersLifecycleOutsidePerSessionAndReconcilesIdempotently(t *testing.T) {
	store := newMemoryBindingStore()
	starts := make(chan StartRequest, 8)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		return &managerFixtureSession{threadID: "thread-" + string(request.Key.SessionID), started: make(chan string, 1), release: make(chan struct{})}, nil
	}, store)
	defer manager.Close(context.Background())
	active, archived := domain.SessionID("active"), domain.SessionID("archived")
	if err := manager.SetMode(context.Background(), ModeDisabled); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), []PrimaryState{{SessionID: active, Desired: DesiredActive}, {SessionID: archived, Desired: DesiredArchived}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	started := receiveStart(t, starts)
	if started.Key.SessionID != active {
		t.Fatalf("started primary = %q, want active", started.Key.SessionID)
	}
	select {
	case extra := <-starts:
		t.Fatalf("archived primary started: %#v", extra)
	case <-time.After(30 * time.Millisecond):
	}
	if err := manager.Reconcile(context.Background(), []PrimaryState{{SessionID: active, Desired: DesiredActive}, {SessionID: archived, Desired: DesiredArchived}}); err != nil {
		t.Fatal(err)
	}
	select {
	case extra := <-starts:
		t.Fatalf("idempotent reconcile duplicated start: %#v", extra)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestManagerPersistsDesiredActiveBeforeReturningStartFailureAndCanRetry(t *testing.T) {
	store := newMemoryBindingStore()
	startErr := errors.New("satellite start failed")
	attempts := 0
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		attempts++
		if attempts == 1 {
			return nil, startErr
		}
		return &managerFixtureSession{threadID: "thread-retried", started: make(chan string, 1), release: make(chan struct{})}, nil
	}, store)
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	id := domain.SessionID("main-ready")
	if err := manager.Activate(context.Background(), id); !errors.Is(err, startErr) {
		t.Fatalf("activate error = %v, want start failure", err)
	}
	waitBinding(t, store, BindingKey{Mode: ModePerSession, SessionID: id}, DesiredActive, "")
	if err := manager.Activate(context.Background(), id); err != nil {
		t.Fatalf("lazy retry = %v", err)
	}
	waitBinding(t, store, BindingKey{Mode: ModePerSession, SessionID: id}, DesiredActive, "thread-retried")
}

func TestManagerSetSameModeAfterInvalidateRebuildsTopology(t *testing.T) {
	for _, mode := range []Mode{ModeShared, ModePerSession} {
		t.Run(string(mode), func(t *testing.T) {
			starts := make(chan StartRequest, 8)
			manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
				starts <- request
				thread := "thread-shared"
				if request.Key.SessionID != "" {
					thread = "thread-" + string(request.Key.SessionID)
				}
				return &managerFixtureSession{threadID: thread, started: make(chan string, 1), release: make(chan struct{})}, nil
			}, newMemoryBindingStore())
			defer manager.Close(context.Background())
			if mode == ModePerSession {
				if err := manager.SetMode(context.Background(), mode); err != nil {
					t.Fatal(err)
				}
				if err := manager.Activate(context.Background(), "main-a"); err != nil {
					t.Fatal(err)
				}
			} else if err := manager.Warmup(context.Background()); err != nil {
				t.Fatal(err)
			}
			first := receiveStart(t, starts)
			manager.Invalidate()
			if err := manager.SetMode(context.Background(), mode); err != nil {
				t.Fatal(err)
			}
			second := receiveStart(t, starts)
			if second.Key != first.Key {
				t.Fatalf("rebuilt key = %#v, want %#v", second.Key, first.Key)
			}
		})
	}
}

func TestManagerRoutesDurablyCapturedModeAfterSettingsTransition(t *testing.T) {
	starts := make(chan StartRequest, 4)
	turns := make(chan string, 4)
	release := make(chan struct{})
	close(release)
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		thread := "captured-shared"
		if request.Key.Mode == ModePerSession {
			thread = "current-personal"
		}
		return &managerFixtureSession{threadID: thread, started: turns, release: release}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	request := satelliteRequest(ModeShared, "main-a", "accepted-before-transition", 1)
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Process(context.Background(), request)
	if err != nil || result.Completion == nil {
		t.Fatalf("captured request = %#v, %v", result, err)
	}
	if start := receiveStart(t, starts); start.Key != (BindingKey{Mode: ModeShared}) {
		t.Fatalf("captured request routed to %#v", start.Key)
	}
	if turn := receiveString(t, turns); turn != "captured-shared:accepted-before-transition:1" {
		t.Fatalf("captured turn = %q", turn)
	}
	if err := result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerFinishesAdmittedOldModeAndRetiresItOnlyAfterAcceptance(t *testing.T) {
	starts := make(chan StartRequest, 4)
	turns, closed := make(chan string, 4), make(chan string, 4)
	release := make(chan struct{})
	manager := newManager(func(_ context.Context, request StartRequest) (Session, error) {
		starts <- request
		return &managerFixtureSession{threadID: "old-shared", started: turns, release: release, closed: closed}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	request := satelliteRequest(ModeShared, "main-a", "admitted-old-mode", 1)
	type outcome struct {
		result promptpreprocess.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := manager.Process(context.Background(), request)
		done <- outcome{result: result, err: err}
	}()
	if start := receiveStart(t, starts); start.Key.Mode != ModeShared {
		t.Fatalf("initial topology = %#v", start.Key)
	}
	if turn := receiveString(t, turns); turn != "old-shared:admitted-old-mode:1" {
		t.Fatalf("turn = %q", turn)
	}
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	select {
	case thread := <-closed:
		t.Fatalf("admitted topology closed before result/accept: %q", thread)
	default:
	}
	close(release)
	first := <-done
	if first.err != nil || first.result.Completion == nil {
		t.Fatalf("old admitted result = %#v, %v", first.result, first.err)
	}
	replayed, err := manager.Process(context.Background(), request)
	if err != nil || replayed.Text != first.result.Text || replayed.Completion == nil {
		t.Fatalf("stale replay = %#v, %v", replayed, err)
	}
	select {
	case turn := <-turns:
		t.Fatalf("stale replay reached provider: %q", turn)
	default:
	}
	if err := replayed.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if thread := receiveString(t, closed); thread != "old-shared" {
		t.Fatalf("retired thread = %q", thread)
	}
	if err := first.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerPrimaryLifecycleClosesStalePersonalLaneButKeepsShared(t *testing.T) {
	for _, operation := range []string{"archive", "forget", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			store := newMemoryBindingStore()
			starts := make(chan StartRequest, 8)
			turns, closed := make(chan string, 8), make(chan string, 8)
			personalRelease := make(chan struct{})
			sharedRelease := make(chan struct{})
			close(sharedRelease)
			manager := New(func(_ context.Context, request StartRequest) (Session, error) {
				starts <- request
				if request.Key.Mode == ModeShared {
					return &managerFixtureSession{threadID: "shared", started: turns, release: sharedRelease, closed: closed}, nil
				}
				return &managerFixtureSession{threadID: "personal", started: turns, release: personalRelease, closed: closed}, nil
			}, store)
			defer manager.Close(context.Background())

			id := domain.SessionID("main-a")
			if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
				t.Fatal(err)
			}
			if err := manager.Activate(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if start := receiveStart(t, starts); start.Key.SessionID != id {
				t.Fatalf("personal start = %#v", start)
			}
			personalDone := make(chan error, 1)
			go func() {
				_, err := manager.Process(context.Background(), satelliteRequest(ModePerSession, string(id), "personal", 1))
				personalDone <- err
			}()
			if turn := receiveString(t, turns); turn != "personal:personal:1" {
				t.Fatalf("personal turn = %q", turn)
			}
			if err := manager.SetMode(context.Background(), ModeShared); err != nil {
				t.Fatal(err)
			}
			if start := receiveStart(t, starts); start.Key.Mode != ModeShared {
				t.Fatalf("shared start = %#v", start)
			}

			var err error
			switch operation {
			case "archive":
				err = manager.Archive(context.Background(), id)
			case "forget":
				err = manager.Forget(context.Background(), id)
			case "reconcile":
				err = manager.Reconcile(context.Background(), []PrimaryState{{SessionID: id, Desired: DesiredArchived}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if thread := receiveString(t, closed); thread != "personal" {
				t.Fatalf("closed lane = %q", thread)
			}
			select {
			case thread := <-closed:
				t.Fatalf("primary lifecycle closed shared lane %q", thread)
			default:
			}
			if processErr := <-personalDone; processErr == nil {
				t.Fatal("stale personal request unexpectedly completed")
			}
			key := BindingKey{Mode: ModePerSession, SessionID: id}
			binding, found, loadErr := store.Load(context.Background(), key)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if operation == "forget" {
				if found {
					t.Fatalf("forgotten binding remains: %#v", binding)
				}
			} else if !found || binding.Desired != DesiredArchived {
				t.Fatalf("archived binding = %#v, found=%t", binding, found)
			}

			shared, processErr := manager.Process(context.Background(), satelliteRequest(ModeShared, string(id), "shared", 2))
			if processErr != nil || shared.Completion == nil {
				t.Fatalf("shared result = %#v, %v", shared, processErr)
			}
			if err := shared.Completion.Accept(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagerSameModeSharedRebuildWaitsForOldAcceptance(t *testing.T) {
	starts := make(chan StartRequest, 8)
	turns, closed := make(chan string, 8), make(chan string, 8)
	firstRelease := make(chan struct{})
	readyRelease := make(chan struct{})
	close(readyRelease)
	startCount := 0
	manager := New(func(_ context.Context, request StartRequest) (Session, error) {
		startCount++
		thread := fmt.Sprintf("shared-%d", startCount)
		release := readyRelease
		if startCount == 1 {
			release = firstRelease
		}
		starts <- request
		return &managerFixtureSession{threadID: thread, started: turns, release: release, closed: closed}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	if err := manager.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	receiveStart(t, starts)

	type outcome struct {
		result promptpreprocess.Result
		err    error
	}
	firstDone := make(chan outcome, 1)
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModeShared, "main-a", "first", 1))
		firstDone <- outcome{result: result, err: err}
	}()
	if turn := receiveString(t, turns); turn != "shared-1:first:1" {
		t.Fatalf("first turn = %q", turn)
	}
	manager.Invalidate()
	if err := manager.SetMode(context.Background(), ModeShared); err != nil {
		t.Fatal(err)
	}
	secondDone := make(chan outcome, 1)
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModeShared, "main-b", "second", 1))
		secondDone <- outcome{result: result, err: err}
	}()
	assertNoStart(t, starts, "replacement before old result")
	close(firstRelease)
	first := <-firstDone
	if first.err != nil || first.result.Completion == nil {
		t.Fatalf("first result = %#v, %v", first.result, first.err)
	}
	assertNoStart(t, starts, "replacement before durable acceptance")
	if err := first.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	receiveStart(t, starts)
	if thread := receiveString(t, closed); thread != "shared-1" {
		t.Fatalf("retired thread = %q", thread)
	}
	second := <-secondDone
	if second.err != nil || second.result.Completion == nil {
		t.Fatalf("second result = %#v, %v", second.result, second.err)
	}
	if err := second.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestManagerInvalidationKeepsIdleSlotAsPredecessorUntilCloseCompletes(t *testing.T) {
	starts := make(chan StartRequest, 4)
	closeStarted := make(chan struct{}, 1)
	closeRelease := make(chan struct{})
	turnRelease := make(chan struct{})
	close(turnRelease)
	count := 0
	manager := New(func(_ context.Context, request StartRequest) (Session, error) {
		count++
		starts <- request
		session := &managerFixtureSession{threadID: "same-thread", started: make(chan string, 4), release: turnRelease}
		if count == 1 {
			session.closeStarted = closeStarted
			session.closeRelease = closeRelease
		}
		return session, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	if err := manager.Warmup(context.Background()); err != nil {
		t.Fatal(err)
	}
	receiveStart(t, starts)
	invalidationDone := make(chan struct{})
	go func() {
		manager.Invalidate()
		close(invalidationDone)
	}()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("old idle satellite close did not start")
	}
	processed := make(chan error, 1)
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModeShared, "main", "after-invalidate", 1))
		if err == nil {
			err = result.Completion.Accept(context.Background())
		}
		processed <- err
	}()
	assertNoStart(t, starts, "replacement while old idle satellite is still closing")
	close(closeRelease)
	select {
	case <-invalidationDone:
	case <-time.After(time.Second):
		t.Fatal("invalidation did not finish")
	}
	receiveStart(t, starts)
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
}

func TestManagerSameModePerSessionRebuildSerializesOnlyMatchingPrimary(t *testing.T) {
	starts := make(chan StartRequest, 16)
	turns := make(chan string, 16)
	oldARelease := make(chan struct{})
	readyRelease := make(chan struct{})
	close(readyRelease)
	counts := map[domain.SessionID]int{}
	var countsMu sync.Mutex
	manager := New(func(_ context.Context, request StartRequest) (Session, error) {
		id := request.Key.SessionID
		countsMu.Lock()
		counts[id]++
		count := counts[id]
		countsMu.Unlock()
		thread := fmt.Sprintf("%s-%d", id, count)
		release := readyRelease
		if id == "main-a" && count == 1 {
			release = oldARelease
		}
		starts <- request
		return &managerFixtureSession{threadID: thread, started: turns, release: release}, nil
	}, newMemoryBindingStore())
	defer manager.Close(context.Background())
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.SessionID{"main-a", "main-b"} {
		if err := manager.Activate(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		receiveStart(t, starts)
	}

	type outcome struct {
		result promptpreprocess.Result
		err    error
	}
	oldDone := make(chan outcome, 1)
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModePerSession, "main-a", "old", 1))
		oldDone <- outcome{result: result, err: err}
	}()
	if turn := receiveString(t, turns); turn != "main-a-1:old:1" {
		t.Fatalf("old A turn = %q", turn)
	}
	manager.Invalidate()
	if err := manager.SetMode(context.Background(), ModePerSession); err != nil {
		t.Fatal(err)
	}
	if start := receiveStart(t, starts); start.Key.SessionID != "main-b" {
		t.Fatalf("parallel replacement = %#v, want main-b", start)
	}

	newADone := make(chan outcome, 1)
	go func() {
		result, err := manager.Process(context.Background(), satelliteRequest(ModePerSession, "main-a", "new", 2))
		newADone <- outcome{result: result, err: err}
	}()
	assertNoStart(t, starts, "main-a replacement before old acceptance")
	close(oldARelease)
	old := <-oldDone
	if old.err != nil || old.result.Completion == nil {
		t.Fatalf("old result = %#v, %v", old.result, old.err)
	}
	assertNoStart(t, starts, "main-a replacement before durable acceptance")
	if err := old.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if start := receiveStart(t, starts); start.Key.SessionID != "main-a" {
		t.Fatalf("replacement after acceptance = %#v", start)
	}
	newA := <-newADone
	if newA.err != nil || newA.result.Completion == nil {
		t.Fatalf("new A result = %#v, %v", newA.result, newA.err)
	}
	if err := newA.result.Completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func assertNoStart(t *testing.T, starts <-chan StartRequest, stage string) {
	t.Helper()
	select {
	case start := <-starts:
		t.Fatalf("%s: %#v", stage, start)
	case <-time.After(30 * time.Millisecond):
	}
}

func receiveString(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lifecycle event")
		return ""
	}
}

func receiveStart(t *testing.T, values <-chan StartRequest) StartRequest {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for satellite start")
		return StartRequest{}
	}
}

func waitBinding(t *testing.T, store *memoryBindingStore, key BindingKey, desired DesiredState, threadID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		binding, found, _ := store.Load(context.Background(), key)
		if found && binding.Desired == desired && binding.ProviderSessionID == threadID {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("binding = %#v, want desired=%q thread=%q", store.binding(t, key), desired, threadID)
}
