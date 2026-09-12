package telegramcontroller_test

import (
	"context"
	"fmt"
	"testing"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

type countingSessionStore struct {
	telegramcontroller.SessionStore
	loads            int
	lists            int
	eligibilityLoads int
	projectionLoads  int
	empty            map[domain.SessionID]bool
}

func (store *countingSessionStore) Load(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	store.loads++
	return store.SessionStore.Load(ctx, id)
}

func (store *countingSessionStore) List(ctx context.Context) ([]domain.Session, error) {
	store.lists++
	return store.SessionStore.List(ctx)
}

func (store *countingSessionStore) reset() {
	store.loads, store.lists, store.eligibilityLoads, store.projectionLoads = 0, 0, 0, 0
}

func (store *countingSessionStore) LoadCardProjectionSnapshot(ctx context.Context, _ domain.SessionID) (cardtranscript.Snapshot, int, int, string, bool, bool, []domain.Session, map[domain.SessionID]bool, error) {
	store.projectionLoads++
	sessions, err := store.SessionStore.List(ctx)
	if err != nil {
		return cardtranscript.Snapshot{}, 0, 0, "", false, false, nil, nil, err
	}
	return cardtranscript.Snapshot{}, 1, 1, "", true, true, sessions, store.empty, nil
}

func (store *countingSessionStore) ListWithEmptyCloseEligibility(ctx context.Context) ([]domain.Session, map[domain.SessionID]bool, error) {
	store.eligibilityLoads++
	sessions, err := store.SessionStore.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[domain.SessionID]bool, len(store.empty))
	for id, empty := range store.empty {
		result[id] = empty
	}
	return sessions, result, nil
}

func TestSemanticNavigationBuildsSessionCardOncePerCallback(t *testing.T) {
	first := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, t.TempDir(), "provider-1", 1)
	second := readySession(t, "22222222-2222-4222-9222-222222222222", domain.ProviderCodex, t.TempDir(), "provider-2", 1)
	sessions := []domain.Session{first, second}
	empty := make(map[domain.SessionID]bool)
	for index := 0; index < 7; index++ {
		id := domain.SessionID(fmt.Sprintf("%08d-3333-4333-9333-333333333333", index+1))
		standby := readySession(t, string(id), domain.ProviderCodex, t.TempDir(), fmt.Sprintf("standby-provider-%d", index), 1)
		snapshot := standby.Snapshot()
		snapshot.IntentID = domain.IntentID(fmt.Sprintf("standby:%d", index))
		standby, err := domain.RestoreSession(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, standby)
		empty[id] = true
	}
	base := newLockedSessions(sessions...)
	store := &countingSessionStore{SessionStore: base, empty: empty}
	controller := newController(t, creatorFunc(nil), store, submitterFunc(nil), notifierFunc(nil), telegramcontroller.Options{Recovered: sessions})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	store.reset()
	selected, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSelect, SessionID: second.ID()})
	if err != nil || selected.Card == nil || selected.Card.SessionID != second.ID() {
		t.Fatalf("select callback = (%#v, %v)", selected, err)
	}
	if store.loads > 1 || store.lists != 0 {
		t.Fatalf("select projection reads = load:%d list:%d, want at most 1/0", store.loads, store.lists)
	}
	if store.projectionLoads != 1 || store.eligibilityLoads != 0 {
		t.Fatalf("select projection snapshots/legacy eligibility = %d/%d, want 1/0", store.projectionLoads, store.eligibilityLoads)
	}

	store.reset()
	paged, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPagePrevious, SessionID: second.ID(), Page: 1})
	if err != nil || paged.Card == nil || paged.Card.SessionID != second.ID() {
		t.Fatalf("page callback = (%#v, %v)", paged, err)
	}
	if store.loads != 1 || store.lists != 0 {
		t.Fatalf("page projection reads = load:%d list:%d, want 1/0", store.loads, store.lists)
	}
	if store.projectionLoads != 1 || store.eligibilityLoads != 0 {
		t.Fatalf("page projection snapshots/legacy eligibility = %d/%d, want 1/0", store.projectionLoads, store.eligibilityLoads)
	}
}
