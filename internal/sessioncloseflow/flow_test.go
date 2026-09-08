package sessioncloseflow_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessioncloseflow"
	"bria/internal/telegramnodes"
)

type observer struct{ events []controllertelemetry.Event }

func (o *observer) ObserveControllerEvent(_ context.Context, e controllertelemetry.Event) {
	o.events = append(o.events, e)
}

type sessions struct{ session domain.Session }

func (s sessions) Load(context.Context, domain.SessionID) (domain.Session, error) {
	return s.session, nil
}
func (s sessions) List(context.Context) ([]domain.Session, error) {
	return []domain.Session{s.session}, nil
}

func TestBusyOperationSurvivesCancellationAndIsConsumedOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(controllertelemetry.WithOperation(context.Background(), "first-close"))
	starting, _ := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "intent", "local", domain.ProviderCodex, "/synthetic")
	ready, _ := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider", Generation: 1})
	running, err := ready.StartWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	closing, err := running.CloseAfterWork(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var f sessioncloseflow.Flow
	closeFn := func(context.Context, domain.SessionID) (app.CloseSessionResult, error) {
		return app.CloseSessionResult{Session: closing, Scheduled: true}, nil
	}
	if _, err := f.Close(ctx, closing.ID(), closeFn); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := f.Close(controllertelemetry.WithOperation(context.Background(), "repeated-close"), closing.ID(), closeFn); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("repeated close rejected")
	for _, operation := range []string{"failed-repeat", "first-close"} {
		_, err := f.Close(controllertelemetry.WithOperation(context.Background(), operation), closing.ID(), func(context.Context, domain.SessionID) (app.CloseSessionResult, error) {
			return app.CloseSessionResult{}, failure
		})
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	completed := f.CompletionContext(context.Background(), closing.ID())
	if completed.Err() != nil || controllertelemetry.Operation(completed) != "first-close" {
		t.Fatal("lost initiating close correlation")
	}
	if controllertelemetry.Operation(f.CompletionContext(context.Background(), closing.ID())) != "" {
		t.Fatal("invented repeated completion correlation")
	}
}

func TestSelectionDoesNotClaimPersistenceWithoutStore(t *testing.T) {
	ctx := context.Background()
	s, _ := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "intent", "local", domain.ProviderCodex, "/synthetic")
	nodes, err := telegramnodes.New("local", sessions{s}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	o := &observer{}
	f := sessioncloseflow.Flow{Observer: o}
	if err := f.Selection(ctx, s, nodes, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := o.events[0]; got.Outcome != controllertelemetry.Skipped || got.Reason != controllertelemetry.StoreUnavailable {
		t.Fatalf("false persistence: %+v", got)
	}
	failure := errors.New("private error")
	if err := f.Selection(ctx, s, nodes, func() error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if got := o.events[1]; got.Outcome != controllertelemetry.Failed || got.Reason != controllertelemetry.PersistFailed {
		t.Fatalf("lost failure: %+v", got)
	}
}
