package telegramcontroller_test

import (
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"context"
	"sync/atomic"
	"testing"
)

type countingSessionProjectionStore struct {
	base  *lockedSessions
	loads atomic.Int64
	lists atomic.Int64
}

func (store *countingSessionProjectionStore) Load(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	store.loads.Add(1)
	return store.base.Load(ctx, id)
}

func (store *countingSessionProjectionStore) List(ctx context.Context) ([]domain.Session, error) {
	store.lists.Add(1)
	return store.base.List(ctx)
}

func (store *countingSessionProjectionStore) reset() {
	store.loads.Store(0)
	store.lists.Store(0)
}

func TestSemanticPaginationBuildsSessionCardOnce(t *testing.T) {
	session := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	sessions := &countingSessionProjectionStore{base: newLockedSessions(session)}
	state := &projectionUIState{history: map[domain.SessionID][]string{session.ID(): {"one", "two"}}}
	controller := newController(t, creatorFunc(nil), sessions, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Recovered: []domain.Session{session}, UIState: state,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	sessions.reset()

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPageLatest, SessionID: session.ID(), FollowLatest: true})
	if err != nil || result.Card == nil {
		t.Fatalf("page latest = %#v, %v", result, err)
	}
	if got := sessions.loads.Load(); got != 1 {
		t.Fatalf("session loads = %d, want one card projection", got)
	}
	if got := sessions.lists.Load(); got != 1 {
		t.Fatalf("session lists = %d, want one card projection", got)
	}
}

func TestSemanticSessionSwitchBuildsSelectedCardOnce(t *testing.T) {
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderClaude, t.TempDir(), "provider-2", 1)
	sessions := &countingSessionProjectionStore{base: newLockedSessions(first, second)}
	controller := newController(t, creatorFunc(nil), sessions, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{
		Recovered: []domain.Session{first, second}, UIState: &recordingActiveStore{},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	sessions.reset()

	result, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: second.ID()})
	if err != nil || result.Card == nil || result.Card.SessionID != second.ID() {
		t.Fatalf("select session = %#v, %v", result, err)
	}
	if got := sessions.loads.Load(); got != 1 {
		t.Fatalf("session loads = %d, want one selection/projection load", got)
	}
	if got := sessions.lists.Load(); got != 1 {
		t.Fatalf("session lists = %d, want one card projection", got)
	}
}
