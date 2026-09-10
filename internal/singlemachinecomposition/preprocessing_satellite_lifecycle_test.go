package singlemachinecomposition

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/promptpreprocesssession"
	"bria/internal/settings"
)

type recordingSatelliteLifecycle struct {
	actions []string
	err     error
}

func (lifecycle *recordingSatelliteLifecycle) Reconcile(context.Context, []promptpreprocesssession.PrimaryState) error {
	return lifecycle.err
}

func (lifecycle *recordingSatelliteLifecycle) record(action string, id domain.SessionID) error {
	lifecycle.actions = append(lifecycle.actions, action+":"+string(id))
	return lifecycle.err
}
func (lifecycle *recordingSatelliteLifecycle) Activate(_ context.Context, id domain.SessionID) error {
	return lifecycle.record("activate", id)
}
func (lifecycle *recordingSatelliteLifecycle) Archive(_ context.Context, id domain.SessionID) error {
	return lifecycle.record("archive", id)
}
func (lifecycle *recordingSatelliteLifecycle) Restore(_ context.Context, id domain.SessionID) error {
	return lifecycle.record("restore", id)
}
func (lifecycle *recordingSatelliteLifecycle) Forget(_ context.Context, id domain.SessionID) error {
	return lifecycle.record("forget", id)
}

func TestPreprocessingSatelliteStatesMapsOnlyArchivedToArchived(t *testing.T) {
	ready := satelliteReadySession(t, "77777777-7777-4777-9777-777777777777")
	remoteStarting, err := domain.NewStartingSessionAt("88888888-8888-4888-9888-888888888888", "intent:remote", "remote", domain.ProviderCodex, t.TempDir(), ready.StateChangedAt(), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := remoteStarting.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-remote", Generation: 1}, remoteStarting.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	archiveReady := satelliteReadySession(t, "99999999-9999-4999-9999-999999999999")
	archived, err := archiveReady.BeginClose(archiveReady.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	archived, err = archived.Archive(archived.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	states := preprocessingSatelliteStates("local", []domain.Session{ready, remote, archived})
	if len(states) != 2 || states[0].Desired != promptpreprocesssession.DesiredActive || states[1].Desired != promptpreprocesssession.DesiredArchived {
		t.Fatalf("satellite states = %#v", states)
	}
}

func TestSatellitePreprocessingModeDefaultsToShared(t *testing.T) {
	for _, test := range []struct {
		mode settings.SatellitePreprocessingMode
		want promptpreprocesssession.Mode
	}{
		{settings.SatellitePreprocessingDisabled, promptpreprocesssession.ModeDisabled},
		{settings.SatellitePreprocessingShared, promptpreprocesssession.ModeShared},
		{settings.SatellitePreprocessingPerSession, promptpreprocesssession.ModePerSession},
		{"", promptpreprocesssession.ModeShared},
	} {
		if got := mapSatellitePreprocessingMode(test.mode); got != test.want {
			t.Fatalf("map mode %q = %q, want %q", test.mode, got, test.want)
		}
	}
}

type archivedResumerFunc func(context.Context, domain.SessionID) (domain.Session, error)

func (function archivedResumerFunc) Resume(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return function(ctx, id)
}

type closeWorkflow struct {
	result     app.CloseSessionResult
	err        error
	completion func(context.Context, app.CloseSessionResult, error)
}

func (workflow *closeWorkflow) Close(context.Context, domain.SessionID) (app.CloseSessionResult, error) {
	return workflow.result, workflow.err
}
func (workflow *closeWorkflow) BeginCloseWithCompletion(_ context.Context, _ domain.SessionID, completed func(context.Context, app.CloseSessionResult, error)) (app.CloseSessionResult, error) {
	workflow.completion = completed
	return workflow.result, workflow.err
}

func TestSatelliteCreationActivationDoesNotChangeMainSessionOutcome(t *testing.T) {
	ready := satelliteReadySession(t, "11111111-1111-4111-9111-111111111111")
	satellites := &recordingSatelliteLifecycle{err: errors.New("satellite start failed")}
	var reported error
	applySatelliteCreation(context.Background(), satellites, func(_ context.Context, action string, id domain.SessionID, err error) {
		if action != "activate" || id != ready.ID() {
			t.Fatalf("report = %s/%s", action, id)
		}
		reported = err
	}, app.CreateSessionResult{Session: ready}, nil)
	if len(satellites.actions) != 1 || satellites.actions[0] != "activate:"+string(ready.ID()) || !errors.Is(reported, satellites.err) {
		t.Fatalf("activation actions=%v report=%v", satellites.actions, reported)
	}
	applySatelliteCreation(context.Background(), satellites, nil, app.CreateSessionResult{Session: ready, StartError: errors.New("main failed")}, nil)
	if len(satellites.actions) != 1 {
		t.Fatal("failed main session activated a satellite")
	}
}

func TestSatelliteRestoreFollowsSuccessfulMainRestore(t *testing.T) {
	ready := satelliteReadySession(t, "22222222-2222-4222-9222-222222222222")
	satellites := &recordingSatelliteLifecycle{}
	resumer := satelliteArchivedResumer{
		base:       archivedResumerFunc(func(context.Context, domain.SessionID) (domain.Session, error) { return ready, nil }),
		satellites: satellites,
	}
	got, err := resumer.Resume(context.Background(), ready.ID())
	if err != nil || !got.Equal(ready) || len(satellites.actions) != 1 || satellites.actions[0] != "restore:"+string(ready.ID()) {
		t.Fatalf("restore = (%#v,%v), actions=%v", got, err, satellites.actions)
	}
}

func TestSatelliteCloseArchivesOnlyAfterMainArchiveAndForgetsDeleted(t *testing.T) {
	ready := satelliteReadySession(t, "33333333-3333-4333-9333-333333333333")
	closing, err := ready.BeginClose(ready.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(closing.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	satellites := &recordingSatelliteLifecycle{}
	workflow := &closeWorkflow{result: app.CloseSessionResult{Session: closing, Scheduled: true}}
	closer := satelliteSessionCloser{base: workflow, satellites: satellites}
	completedCalls := 0
	if _, err := closer.BeginCloseWithCompletion(context.Background(), ready.ID(), func(_ context.Context, result app.CloseSessionResult, err error) {
		completedCalls++
		if err != nil || result.Session.Status() != domain.SessionArchived {
			t.Fatalf("forwarded completion = %#v, %v", result, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(satellites.actions) != 0 {
		t.Fatal("satellite archived before main close completed")
	}
	workflow.completion(context.Background(), app.CloseSessionResult{Session: archived}, nil)
	if len(satellites.actions) != 1 || satellites.actions[0] != "archive:"+string(ready.ID()) || completedCalls != 1 {
		t.Fatalf("archive actions=%v completion calls=%d", satellites.actions, completedCalls)
	}
	workflow.result = app.CloseSessionResult{Session: ready, Deleted: true}
	if _, err := closer.Close(context.Background(), ready.ID()); err != nil {
		t.Fatal(err)
	}
	if len(satellites.actions) != 2 || satellites.actions[1] != "forget:"+string(ready.ID()) {
		t.Fatalf("deleted actions=%v", satellites.actions)
	}
}

type blockingArchiveLifecycle struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (blockingArchiveLifecycle) Activate(context.Context, domain.SessionID) error { return nil }
func (blockingArchiveLifecycle) Restore(context.Context, domain.SessionID) error  { return nil }
func (blockingArchiveLifecycle) Forget(context.Context, domain.SessionID) error   { return nil }
func (blockingArchiveLifecycle) Reconcile(context.Context, []promptpreprocesssession.PrimaryState) error {
	return nil
}
func (lifecycle blockingArchiveLifecycle) Archive(ctx context.Context, _ domain.SessionID) error {
	lifecycle.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lifecycle.release:
		return nil
	}
}

func TestSatelliteCleanupCannotDelayMainArchiveCompletion(t *testing.T) {
	ready := satelliteReadySession(t, "44444444-4444-4444-9444-444444444444")
	closing, err := ready.BeginClose(ready.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(closing.StateChangedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	workflow := &closeWorkflow{result: app.CloseSessionResult{Session: closing, Scheduled: true}}
	closer := satelliteSessionCloser{base: workflow, satellites: blockingArchiveLifecycle{started: started, release: release}}
	completed := make(chan struct{}, 1)
	if _, err := closer.BeginCloseWithCompletion(context.Background(), ready.ID(), func(context.Context, app.CloseSessionResult, error) {
		completed <- struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	callbackDone := make(chan struct{})
	go func() {
		workflow.completion(context.Background(), app.CloseSessionResult{Session: archived}, nil)
		close(callbackDone)
	}()
	select {
	case <-completed:
	case <-started:
		t.Fatal("satellite cleanup started before main completion was forwarded")
	case <-time.After(time.Second):
		t.Fatal("main archive completion was not forwarded")
	}
	close(release)
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("satellite cleanup did not finish")
	}
}

func satelliteReadySession(t *testing.T, id domain.SessionID) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSessionAt(id, domain.IntentID("intent:"+string(id)), "local", domain.ProviderCodex, t.TempDir(), time.Now().UTC(), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + string(id), Generation: 1}, time.Now().UTC().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return ready
}
