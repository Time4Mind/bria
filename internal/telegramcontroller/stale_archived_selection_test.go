package telegramcontroller_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type blockingSelectionStore struct {
	*lockedSessions
	target  domain.SessionID
	armed   bool
	once    sync.Once
	loaded  chan struct{}
	release chan struct{}
}

func (store *blockingSelectionStore) Load(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	session, err := store.lockedSessions.Load(ctx, id)
	if err == nil && store.armed && id == store.target {
		store.once.Do(func() { close(store.loaded) })
		select {
		case <-store.release:
		case <-ctx.Done():
			return domain.Session{}, ctx.Err()
		}
	}
	return session, err
}

func TestStaleArchivedSelectionReturnsSessionListAndKeepsCurrentSession(t *testing.T) {
	current := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-current", 1)
	prior := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderCodex, t.TempDir(), "provider-prior", 1)
	archived := archivedSession(t, string(prior.ID()), prior.Provider(), prior.Workdir(), "provider-prior", 1)
	controller := newController(t, nil, newLockedSessions(current, archived), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{current}})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSelect, SessionID: archived.ID(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Card != nil || result.Surface == nil || !strings.HasPrefix(result.Surface.Text, "Сессии") {
		t.Fatalf("stale archived selection = %#v, want session-list surface", result)
	}
	projected, err := controller.ProjectCurrent(context.Background(), "")
	if err != nil || projected.Card == nil || projected.Card.SessionID != current.ID() {
		t.Fatalf("current projection after stale selection = (%#v, %v), want %q", projected, err, current.ID())
	}
}

func TestSelectionRevalidatesStatusBeforePersistingActiveSession(t *testing.T) {
	current := readySession(t, "33333333-3333-4333-9333-333333333333", domain.ProviderCodex, t.TempDir(), "provider-current", 1)
	target := readySession(t, "44444444-4444-4444-9444-444444444444", domain.ProviderCodex, t.TempDir(), "provider-target", 1)
	archived := archivedSession(t, string(target.ID()), target.Provider(), target.Workdir(), "provider-target", 1)
	store := &blockingSelectionStore{lockedSessions: newLockedSessions(current, target), target: target.ID(), loaded: make(chan struct{}), release: make(chan struct{})}
	archivedPersisted := make(chan struct{})
	controller := newController(t, nil, store, nil, nil, telegramcontroller.Options{
		Recovered: []domain.Session{current, target},
		SessionCloser: sessionCloserFunc(func(context.Context, domain.SessionID) (app.CloseSessionResult, error) {
			store.Set(archived)
			close(archivedPersisted)
			return app.CloseSessionResult{Session: archived}, nil
		}),
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	if _, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: current.ID()}); err != nil {
		t.Fatal(err)
	}
	store.armed = true
	type result struct{ err error }
	selectionDone := make(chan result, 1)
	go func() {
		_, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: target.ID()})
		selectionDone <- result{err: err}
	}()
	waitControllerSignal(t, store.loaded, "selection did not load target")
	closeDone := make(chan result, 1)
	go func() {
		_, err := controller.CloseSession(context.Background(), target.ID())
		closeDone <- result{err: err}
	}()
	waitControllerSignal(t, archivedPersisted, "archive was not persisted")
	close(store.release)
	for _, done := range []<-chan result{selectionDone, closeDone} {
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatal(got.err)
			}
		case <-time.After(time.Second):
			t.Fatal("selection/archive race did not finish")
		}
	}
	projected, err := controller.ProjectCurrent(context.Background(), "")
	if err != nil || projected.Card == nil || projected.Card.SessionID != current.ID() {
		t.Fatalf("active after revalidation = (%#v, %v), want %q", projected, err, current.ID())
	}
}

func waitControllerSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
