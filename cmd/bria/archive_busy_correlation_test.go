package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

// An accepted busy close immediately selects a fallback/menu while its request
// continues in the background. Archive completion retains the close operation.
func TestBusyArchiveSelectsImmediatelyAndRetainsCloseCorrelation(t *testing.T) {
	for _, scenario := range []struct {
		name                string
		stopping, remaining bool
	}{
		{"running", false, true}, {"stopping", true, true}, {"no_remaining", false, false}, {"stopping_no_remaining", true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			ids := []string{"11111111-1111-4111-9111-111111111111"}
			if scenario.remaining {
				ids = append(ids, "22222222-2222-4222-9222-222222222222")
			}
			store, sessions := archiveFixture(t, ids...)
			current, fallback := sessions[0].ID(), domain.SessionID("")
			if scenario.remaining {
				fallback = sessions[1].ID()
			}
			lifecycle, err := app.NewSessionTurnLifecycle(store, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			provider := &busyArchiveProvider{}
			closer, err := app.NewSessionCloser(store, provider, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			started, finish := make(chan struct{}), make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(finish) }) }
			defer release()
			var mu sync.Mutex
			var events []controllertelemetry.Event
			finished := make(chan telegramcontroller.Notification, 8)
			const operation = "busy-close-operation"
			controller, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store,
				busyArchiveSubmitter{started, finish}, archiveNotifier(func(ctx context.Context, n telegramcontroller.Notification) error {
					if n.Kind == telegramcontroller.NotificationFinal || n.Kind == telegramcontroller.NotificationError {
						finished <- n
					}
					return nil
				}), telegramcontroller.Options{Recovered: sessions, UIState: store, TurnLifecycle: lifecycle, SessionCloser: closer,
					ControllerObserver: archiveObserver(func(_ context.Context, e controllertelemetry.Event) {
						mu.Lock()
						defer mu.Unlock()
						events = append(events, e)
					}),
				})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { release(); _ = controller.Close(ctx) }()
			if _, err := controller.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: current}); err != nil {
				t.Fatal(err)
			}
			if _, err := controller.HandleSemanticMessage(ctx, coordinator.Update{ID: 601, Kind: coordinator.UpdateMessage,
				ActorID: 7, ConversationID: 42, ConversationKind: "private", Text: "synthetic running request"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("real controller worker did not enter provider submit")
			}
			wantBefore := domain.SessionRunning
			if scenario.stopping {
				if _, err := lifecycle.BeginStop(ctx, current); err != nil {
					t.Fatal(err)
				}
				wantBefore = domain.SessionStopping
			}
			before, err := store.Load(ctx, current)
			if err != nil || before.Status() != wantBefore {
				t.Fatalf("pre-close status=%s err=%v", before.Status(), err)
			}
			closeContext, cancel := context.WithCancel(controllertelemetry.WithOperation(ctx, operation))
			result, err := controller.HandleSemanticAction(closeContext, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: current, Choice: 1})
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			if scenario.remaining {
				if result.Card == nil || result.Card.SessionID != fallback || result.Card.Archived {
					t.Fatalf("accepted busy close did not immediately project fallback: %+v", result.Card)
				}
			} else if result.Card != nil || result.Surface == nil || result.Surface.Text != "Сессии" {
				t.Fatalf("accepted busy close did not immediately project session menu: %+v", result)
			}
			scheduled, err := store.Load(ctx, current)
			if err != nil || scheduled.Status() != domain.SessionClosingAfterWork {
				t.Fatalf("durable scheduled close=%s err=%v", scheduled.Status(), err)
			}
			active, err := store.LoadActiveSession(ctx)
			if err != nil || active != fallback {
				t.Fatalf("accepted busy close did not persist immediate selection: %q %v", active, err)
			}
			if calls := provider.aborts.Load(); calls != 0 {
				t.Fatalf("busy close aborted provider before request completion: %d", calls)
			}
			select {
			case n := <-finished:
				t.Fatalf("busy close terminated the held request: %+v", n)
			default:
			}
			release()
			// The real worker emits its final only after lifecycle completion.
			select {
			case n := <-finished:
				if n.Kind != telegramcontroller.NotificationFinal || n.Text != "synthetic final" {
					t.Fatalf("worker failed before terminal close: %+v", n)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("real worker did not finish")
			}
			if calls := provider.aborts.Load(); calls != 1 {
				t.Errorf("completed request did not close exact provider once: %d", calls)
			}
			archived, err := store.Load(ctx, current)
			if err != nil || archived.Status() != domain.SessionArchived {
				t.Fatalf("durable final archive=%s err=%v", archived.Status(), err)
			}
			active, err = store.LoadActiveSession(ctx)
			if err != nil || active != fallback {
				t.Fatalf("durable fallback=%q err=%v", active, err)
			}
			projected, err := controller.ProjectCurrent(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			if !scenario.remaining {
				if projected.Card != nil || projected.Surface == nil || projected.Surface.Text != "Сессии" {
					t.Errorf("empty selection did not project session menu: %+v", projected)
				}
			} else if projected.Card == nil || projected.Card.SessionID != fallback || projected.Card.Archived {
				t.Fatalf("current projection after worker close=%+v err=%v", projected.Card, err)
			}
			mu.Lock()
			recorded := append([]controllertelemetry.Event(nil), events...)
			mu.Unlock()
			want := []struct {
				stage   controllertelemetry.Stage
				outcome controllertelemetry.Outcome
			}{
				{controllertelemetry.ArchiveOutcome, controllertelemetry.Scheduled},
				{controllertelemetry.FallbackChoice, controllertelemetry.Selected},
				{controllertelemetry.SelectionPersist, controllertelemetry.Persisted},
				{controllertelemetry.ProjectedTarget, controllertelemetry.Projected},
				{controllertelemetry.ArchiveOutcome, controllertelemetry.Archived},
			}
			if !scenario.remaining {
				want[1].outcome = controllertelemetry.Cleared
			}
			next := 0
			for _, e := range recorded {
				if next == len(want) || e.Stage != want[next].stage || e.Outcome != want[next].outcome {
					continue
				}
				if e.OperationID != operation {
					t.Errorf("%s:%s lost close operation: %q", e.Stage, e.Outcome, e.OperationID)
				}
				if e.Stage == controllertelemetry.FallbackChoice || e.Stage == controllertelemetry.SelectionPersist {
					if e.TargetSessionID != string(fallback) {
						t.Errorf("%s target=%q want=%q", e.Stage, e.TargetSessionID, fallback)
					}
				}
				next++
			}
			if next != len(want) {
				t.Fatalf("incomplete real worker close chain: %d/%d; events=%+v", next, len(want), recorded)
			}
		})
	}
}

type busyArchiveProvider struct {
	archiveStarter
	aborts atomic.Int32
}

func (p *busyArchiveProvider) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	p.aborts.Add(1)
	return nil
}

type busyArchiveSubmitter struct {
	started chan<- struct{}
	finish  <-chan struct{}
}

func (s busyArchiveSubmitter) Submit(ctx context.Context, _ domain.SessionID, _ string) (sessionruntime.TurnResult, error) {
	close(s.started)
	select {
	case <-s.finish:
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "synthetic final"}, nil
	case <-ctx.Done():
		return sessionruntime.TurnResult{}, ctx.Err()
	}
}
