package telegramcontroller_test

import (
	"context"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type countingSessionStore struct {
	telegramcontroller.SessionStore
	loads int
	lists int
}

func (store *countingSessionStore) Load(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	store.loads++
	return store.SessionStore.Load(ctx, id)
}

func (store *countingSessionStore) List(ctx context.Context) ([]domain.Session, error) {
	store.lists++
	return store.SessionStore.List(ctx)
}

func (store *countingSessionStore) reset() { store.loads, store.lists = 0, 0 }

func TestSemanticNavigationBuildsSessionCardOncePerCallback(t *testing.T) {
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderCodex, t.TempDir(), "provider-2", 1)
	base := newLockedSessions(first, second)
	store := &countingSessionStore{SessionStore: base}
	controller := newController(t, creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Recovered: []domain.Session{first, second}})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	store.reset()
	selected, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: second.ID()})
	if err != nil || selected.Card == nil || selected.Card.SessionID != second.ID() {
		t.Fatalf("select callback = (%#v, %v)", selected, err)
	}
	if store.loads > 2 || store.lists != 1 {
		t.Fatalf("select projection reads = load:%d list:%d, want at most 2/1", store.loads, store.lists)
	}

	store.reset()
	paged, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPagePrevious, SessionID: second.ID(), Page: 1})
	if err != nil || paged.Card == nil || paged.Card.SessionID != second.ID() {
		t.Fatalf("page callback = (%#v, %v)", paged, err)
	}
	if store.loads != 1 || store.lists != 1 {
		t.Fatalf("page projection reads = load:%d list:%d, want 1/1", store.loads, store.lists)
	}
}
