package app_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

func TestTurnLifecyclePersistsRunningStoppingAndReady(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	store := &lifecycleStore{session: readySession(t, now.Add(-time.Hour))}
	clock := &stepClock{now: now}
	tracker, err := app.NewSessionTurnLifecycle(store, clock.Now)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := tracker.Start(context.Background(), store.session.ID()); err != nil || got.Status() != domain.SessionRunning {
		t.Fatalf("Start() = %q, %v", got.Status(), err)
	}
	clock.Advance(time.Second)
	if got, err := tracker.BeginStop(context.Background(), store.session.ID()); err != nil || got.Status() != domain.SessionStopping {
		t.Fatalf("BeginStop() = %q, %v", got.Status(), err)
	}
	clock.Advance(time.Second)
	if got, closeAfter, err := tracker.Finish(context.Background(), store.session.ID()); err != nil || closeAfter || got.Status() != domain.SessionReady {
		t.Fatalf("Finish() = %q, %t, %v", got.Status(), closeAfter, err)
	}
	if want := []domain.SessionStatus{domain.SessionRunning, domain.SessionStopping, domain.SessionReady}; !equalStatuses(store.statuses, want) {
		t.Fatalf("statuses = %v, want %v", store.statuses, want)
	}
}

func TestTurnLifecycleFinishPreservesScheduledCloseForCloser(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	running, _ := readySession(t, now.Add(-time.Hour)).StartWork(now.Add(-time.Minute))
	scheduled, _ := running.CloseAfterWork(now)
	store := &lifecycleStore{session: scheduled}
	tracker, _ := app.NewSessionTurnLifecycle(store, func() time.Time { return now.Add(time.Second) })

	got, closeAfter, err := tracker.Finish(context.Background(), scheduled.ID())
	if err != nil || !closeAfter || !got.Equal(scheduled) || store.replaceCalls != 0 {
		t.Fatalf("Finish(scheduled) = %#v, %t, %v, replaces=%d", got.Snapshot(), closeAfter, err, store.replaceCalls)
	}
}

type stepClock struct{ now time.Time }

func (clock *stepClock) Now() time.Time              { return clock.now }
func (clock *stepClock) Advance(delta time.Duration) { clock.now = clock.now.Add(delta) }
