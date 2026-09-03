package sessionexpiry_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionexpiry"
)

func TestSweepClosesOnlyExpiredOpenSessionsInStableOrder(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	readyExpired := session(t, "b", now.Add(-7*time.Hour), domain.SessionLifetime6Hours, domain.SessionReady)
	runningExpired := session(t, "a", now.Add(-7*time.Hour), domain.SessionLifetime6Hours, domain.SessionRunning)
	readyCurrent := session(t, "c", now.Add(-5*time.Hour), domain.SessionLifetime6Hours, domain.SessionReady)
	archivedExpired := session(t, "d", now.Add(-7*time.Hour), domain.SessionLifetime6Hours, domain.SessionArchived)
	source := sourceFunc(func(context.Context) ([]domain.Session, error) {
		return []domain.Session{readyExpired, archivedExpired, readyCurrent, runningExpired}, nil
	})
	var closed []domain.SessionID
	closer := closeFunc(func(_ context.Context, id domain.SessionID) error {
		closed = append(closed, id)
		return nil
	})
	scheduler, err := sessionexpiry.New(source, closer, func() time.Time { return now })
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := scheduler.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if want := []domain.SessionID{"a", "b"}; !reflect.DeepEqual(closed, want) {
		t.Fatalf("closed = %v, want %v", closed, want)
	}
	if !reflect.DeepEqual(result.Closed, closed) || result.Examined != 4 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSweepReportsFailuresAndContinuesOtherExpiredSessions(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	source := sourceFunc(func(context.Context) ([]domain.Session, error) {
		return []domain.Session{
			session(t, "a", now.Add(-7*time.Hour), domain.SessionLifetime6Hours, domain.SessionReady),
			session(t, "b", now.Add(-7*time.Hour), domain.SessionLifetime6Hours, domain.SessionReady),
		}, nil
	})
	closeErr := errors.New("close failed")
	var attempted []domain.SessionID
	closer := closeFunc(func(_ context.Context, id domain.SessionID) error {
		attempted = append(attempted, id)
		if id == "a" {
			return closeErr
		}
		return nil
	})
	scheduler, _ := sessionexpiry.New(source, closer, func() time.Time { return now })

	result, err := scheduler.Sweep(context.Background())
	if !errors.Is(err, closeErr) {
		t.Fatalf("Sweep() error = %v, want %v", err, closeErr)
	}
	if want := []domain.SessionID{"a", "b"}; !reflect.DeepEqual(attempted, want) {
		t.Fatalf("attempted = %v, want %v", attempted, want)
	}
	if want := []domain.SessionID{"b"}; !reflect.DeepEqual(result.Closed, want) {
		t.Fatalf("closed = %v, want %v", result.Closed, want)
	}
	if want := []domain.SessionID{"a"}; !reflect.DeepEqual(result.Failed, want) {
		t.Fatalf("failed = %v, want %v", result.Failed, want)
	}
}

func TestRunSweepsImmediatelyAndStopsWithContext(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := sourceFunc(func(context.Context) ([]domain.Session, error) {
		return []domain.Session{session(t, "a", now.Add(-7*time.Hour), domain.SessionLifetime6Hours, domain.SessionReady)}, nil
	})
	closed := 0
	closer := closeFunc(func(context.Context, domain.SessionID) error {
		closed++
		cancel()
		return nil
	})
	scheduler, _ := sessionexpiry.New(source, closer, func() time.Time { return now })

	err := scheduler.Run(ctx, time.Hour, nil)
	if !errors.Is(err, context.Canceled) || closed != 1 {
		t.Fatalf("Run() = %v, closed=%d, want context cancellation after immediate sweep", err, closed)
	}
}

type sourceFunc func(context.Context) ([]domain.Session, error)

func (function sourceFunc) List(ctx context.Context) ([]domain.Session, error) { return function(ctx) }

type closeFunc func(context.Context, domain.SessionID) error

func (function closeFunc) Close(ctx context.Context, id domain.SessionID) error {
	return function(ctx, id)
}

func session(t *testing.T, id domain.SessionID, createdAt time.Time, lifetime domain.SessionLifetime, status domain.SessionStatus) domain.Session {
	t.Helper()
	value, err := domain.NewStartingSessionAt(id, domain.IntentID("intent-"+id), "local", domain.ProviderCodex, "/work", createdAt, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	value, err = value.ReadyAt(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + string(id), Generation: 1}, createdAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case domain.SessionReady:
		return value
	case domain.SessionRunning:
		value, err = value.StartWork(createdAt.Add(2 * time.Minute))
	case domain.SessionArchived:
		value, err = value.BeginClose(createdAt.Add(2 * time.Minute))
		if err == nil {
			value, err = value.Archive(createdAt.Add(3 * time.Minute))
		}
	default:
		t.Fatalf("unsupported test status %q", status)
	}
	if err != nil {
		t.Fatal(err)
	}
	return value
}
