package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

type archiveCreator struct{}

func (archiveCreator) Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error) {
	return app.CreateSessionResult{}, errors.New("unexpected create")
}

type archiveSubmitter struct{}

func (archiveSubmitter) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("unexpected submit")
}

type archiveNotifier func(context.Context, telegramcontroller.Notification) error
type archiveObserver func(context.Context, controllertelemetry.Event)

func (f archiveObserver) ObserveControllerEvent(ctx context.Context, e controllertelemetry.Event) {
	f(ctx, e)
}

func (f archiveNotifier) Notify(ctx context.Context, n telegramcontroller.Notification) error {
	if f == nil {
		return nil
	}
	return f(ctx, n)
}
func archiveController(t *testing.T, store *storage.SessionStore, notifier archiveNotifier, options telegramcontroller.Options) *telegramcontroller.Controller {
	t.Helper()
	c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, archiveSubmitter{}, notifier, options)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// These fixtures use the durable production store and closer. The barrier only
// chooses the observed ordering: archive finishes before the callback projects.
type archiveBeforeProjection struct {
	closer *app.SessionCloser
	store  *storage.SessionStore
}

func (c archiveBeforeProjection) Close(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
	return c.closer.Close(ctx, id)
}

func (c archiveBeforeProjection) BeginClose(ctx context.Context, id domain.SessionID) (app.CloseSessionResult, error) {
	completion := make(chan error, 1)
	result, err := c.closer.BeginCloseWithCompletion(ctx, id, func(_ context.Context, _ app.CloseSessionResult, err error) { completion <- err })
	if err != nil {
		return result, err
	}
	select {
	case err := <-completion:
		return result, err
	case <-time.After(time.Second):
		return result, errors.New("archive completion callback did not arrive")
	}
}

type archiveStarter struct{}

func (archiveStarter) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("unexpected start")
}
func (archiveStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return nil
}

func archiveFixture(t *testing.T, ids ...string) (*storage.SessionStore, []domain.Session) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions []domain.Session
	for _, id := range ids {
		starting, err := domain.NewStartingSession(domain.SessionID(id), domain.IntentID("intent-"+id), "local", domain.ProviderCodex, "/synthetic")
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.PutStartingIfAbsent(ctx, starting); err != nil {
			t.Fatal(err)
		}
		ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + id, Generation: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CompareAndSwap(ctx, starting, ready); err != nil {
			t.Fatal(err)
		}
		// A completed prompt keeps this an archive, not empty-session deletion.
		if err := store.SetCardPrompt(ctx, ready.ID(), "prompt-"+id, "synthetic prompt"); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, ready)
	}
	return store, sessions
}

func TestInteractiveArchiveBeforeProjectionSelectsRemainingSession(t *testing.T) {
	ctx := context.Background()
	for _, remaining := range []bool{true, false} {
		t.Run(map[bool]string{true: "same_node_fallback", false: "no_remaining"}[remaining], func(t *testing.T) {
			ids := []string{"11111111-1111-4111-9111-111111111111"}
			if remaining {
				ids = append(ids, "22222222-2222-4222-9222-222222222222")
			}
			store, sessions := archiveFixture(t, ids...)
			closer, err := app.NewSessionCloser(store, archiveStarter{}, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			c := archiveController(t, store, nil, telegramcontroller.Options{
				Recovered: sessions, UIState: store, SessionCloser: archiveBeforeProjection{closer, store},
			})
			defer c.Close(ctx)
			if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[0].ID()}); err != nil {
				t.Fatal(err)
			}
			result, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: sessions[0].ID(), Choice: 1})
			if err != nil {
				t.Fatal(err)
			}
			archived, err := store.Load(ctx, sessions[0].ID())
			if err != nil || archived.Status() != domain.SessionArchived {
				t.Fatalf("physical archive: %s, %v", archived.Status(), err)
			}
			want := domain.SessionID("")
			if remaining {
				want = sessions[1].ID()
				if result.Card == nil || result.Card.SessionID != want || result.Card.Archived {
					t.Fatalf("close projected closed card instead of remaining selectable session: %+v", result.Card)
				}
			} else if result.Card != nil || result.Surface == nil || result.Surface.Text != "Сессии" {
				t.Fatalf("close must project empty session menu: %+v", result)
			}
			active, err := store.LoadActiveSession(ctx)
			if err != nil || active != want {
				t.Fatalf("durable active = %q, %v; want %q", active, err, want)
			}
		})
	}
}

type heldArchiveStarter struct{ release <-chan struct{} }

func (heldArchiveStarter) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("unexpected start")
}

type failedArchiveStarter struct{ release <-chan struct{} }

func (failedArchiveStarter) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("unexpected start")
}
func (s failedArchiveStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	<-s.release
	return errors.New("synthetic provider failure with private-text sentinel")
}

func TestInteractiveArchiveFailurePreservesFallbackAndReportsFailure(t *testing.T) {
	ctx := context.Background()
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222")
	release := make(chan struct{})
	closer, err := app.NewSessionCloser(store, failedArchiveStarter{release}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	failed := make(chan controllertelemetry.Event, 4)
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: sessions, UIState: store, SessionCloser: closer, ControllerObserver: archiveObserver(func(_ context.Context, e controllertelemetry.Event) {
		if e.Stage == controllertelemetry.ArchiveOutcome && e.Outcome == controllertelemetry.Failed {
			failed <- e
		}
	})})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[0].ID()}); err != nil {
		t.Fatal(err)
	}
	operationContext := controllertelemetry.WithOperation(ctx, "synthetic-close-operation")
	result, err := c.HandleSemanticAction(operationContext, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: sessions[0].ID(), Choice: 1})
	close(release)
	if err != nil || result.Card == nil || result.Card.SessionID != sessions[1].ID() {
		t.Fatalf("closing projection = %+v, %v", result, err)
	}
	select {
	case event := <-failed:
		if event.Reason != controllertelemetry.CloseFailed || event.OperationID != "synthetic-close-operation" {
			t.Fatalf("async failure correlation = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("provider failure completion not observed")
	}
	current, err := store.Load(ctx, sessions[0].ID())
	if err != nil || current.Status() != domain.SessionAwaitingRecovery {
		t.Fatalf("failed close was lost or falsely archived: %q, %v", current.Status(), err)
	}
	for i := 0; i < 2; i++ {
		active, err := store.LoadActiveSession(ctx)
		if err != nil || active != sessions[1].ID() {
			t.Fatalf("failure/retry changed fallback: %q, %v", active, err)
		}
		if i == 0 {
			_, _ = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: sessions[0].ID(), Choice: 1})
		}
	}
}
func (c heldArchiveStarter) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	<-c.release
	return nil
}

func TestInteractiveArchiveCompletionRefreshesFallbackWithoutManualSelection(t *testing.T) {
	ctx := context.Background()
	store, sessions := archiveFixture(t, "11111111-1111-4111-9111-111111111111", "22222222-2222-4222-9222-222222222222")
	release := make(chan struct{})
	closer, err := app.NewSessionCloser(store, heldArchiveStarter{release}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	notices := make(chan telegramcontroller.Notification, 4)
	completed := make(chan controllertelemetry.Event, 8)
	c := archiveController(t, store, archiveNotifier(func(_ context.Context, n telegramcontroller.Notification) error {
		notices <- n
		return nil
	}), telegramcontroller.Options{Recovered: sessions, UIState: store, SessionCloser: closer, ControllerObserver: archiveObserver(func(_ context.Context, e controllertelemetry.Event) {
		if e.Stage == controllertelemetry.FallbackChoice && e.Outcome == controllertelemetry.Preserved {
			completed <- e
		}
	})})
	defer c.Close(ctx)
	if _, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: sessions[0].ID()}); err != nil {
		t.Fatal(err)
	}
	callbackContext, cancel := context.WithCancel(ctx)
	result, err := c.HandleSemanticAction(callbackContext, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticClose, SessionID: sessions[0].ID(), Choice: 1})
	cancel()
	if err != nil || result.Card == nil || result.Card.SessionID != sessions[1].ID() {
		t.Fatalf("scheduled close = %+v, %v", result, err)
	}
	active, err := store.LoadActiveSession(ctx)
	if err != nil || active != sessions[1].ID() {
		t.Fatalf("fallback was not durable before provider exit: %q, %v", active, err)
	}
	closing, err := store.Load(ctx, sessions[0].ID())
	if err != nil || closing.Status() != domain.SessionClosing {
		t.Fatalf("provider is still held, lifecycle = %q, %v", closing.Status(), err)
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("archive completion did not preserve the already projected selection")
	}
	select {
	case notice := <-notices:
		t.Fatalf("completion reprojected an already detached selection: %+v", notice)
	default:
	}
	active, err = store.LoadActiveSession(ctx)
	if err != nil || active != sessions[1].ID() {
		t.Fatalf("persisted fallback = %q, %v", active, err)
	}
}
