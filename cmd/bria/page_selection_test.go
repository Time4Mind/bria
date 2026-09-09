package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/storage"
	"bria/internal/telegramcontroller"
)

func pageSelectionFixture(t *testing.T) (*telegramcontroller.Controller, *storage.SessionStore, string, domain.Session) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession("11111111-1111-4111-9111-111111111111", "page-selection-intent", "local", domain.ProviderCodex, "/synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(context.Background(), starting); err != nil {
		t.Fatal(err)
	}
	session, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "page-selection-provider", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(context.Background(), starting, session); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"FIRST", "SECOND", "THIRD"} {
		if err := store.AppendCardTypedHistory(context.Background(), session.ID(), label+strings.Repeat("x", 2890), "commentary"); err != nil {
			t.Fatal(err)
		}
	}
	c := archiveController(t, store, nil, telegramcontroller.Options{Recovered: []domain.Session{session}, UIState: store})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c, store, path, session
}

func selectLatestPage(t *testing.T, c *telegramcontroller.Controller, id domain.SessionID) telegramcontroller.SemanticCard {
	t.Helper()
	result, err := c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPageLatest, SessionID: id, FollowLatest: true})
	if err != nil || result.Card == nil {
		t.Fatalf("select latest: %#v %v", result, err)
	}
	return *result.Card
}

func TestPageSelectionLatestFollowsPersistedTranscriptGrowth(t *testing.T) {
	c, store, _, session := pageSelectionFixture(t)
	selected := selectLatestPage(t, c, session.ID())
	if selected.View.Page != 3 || !selected.View.FollowLatest {
		t.Fatalf("initial latest: %#v", selected.View)
	}
	if err := store.AppendCardTypedHistory(context.Background(), session.ID(), "FOURTH"+strings.Repeat("y", 2890), "commentary"); err != nil {
		t.Fatal(err)
	}
	result, err := c.ProjectCurrent(context.Background(), session.ID())
	if err != nil || result.Card == nil {
		t.Fatal(err)
	}
	if result.Card.View.Page != 4 || result.Card.View.Pages != 4 || !result.Card.View.FollowLatest || !strings.HasPrefix(result.Card.Pages[result.Card.View.Page-1].Content, "FOURTH") {
		t.Fatalf("follow stayed behind growth: %#v", result.Card.View)
	}
}

func TestPageSelectionPinsActualAnchorAndRestoresAfterReopen(t *testing.T) {
	ctx := context.Background()
	c, store, path, session := pageSelectionFixture(t)
	selectLatestPage(t, c, session.ID())
	selected, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPagePrevious, SessionID: session.ID(), Page: 2})
	if err != nil || selected.Card == nil || selected.Card.View.Page != 2 || selected.Card.View.FollowLatest {
		t.Fatalf("pin: %#v %v", selected, err)
	}
	want := selected.Card.Pages[1].Content
	saved, err := store.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if page := saved.Cards[session.ID()].Page; page.Anchor != selected.Card.View.Anchor || page.Anchor == "" || page.FollowLatest {
		t.Fatalf("actual selection anchor not persisted: %#v", page)
	}
	if err := store.AppendCardTypedHistory(ctx, session.ID(), "FOURTH"+strings.Repeat("y", 2890), "commentary"); err != nil {
		t.Fatal(err)
	}
	live, err := c.ProjectCurrent(ctx, session.ID())
	if err != nil || live.Card == nil || live.Card.View.FollowLatest || live.Card.Pages[live.Card.View.Page-1].Content != want {
		t.Fatalf("live growth moved pinned page: %#v %v", live, err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	next := archiveController(t, reopened, nil, telegramcontroller.Options{Recovered: []domain.Session{session}, UIState: reopened})
	defer next.Close(ctx)
	result, err := next.ProjectCurrent(ctx, session.ID())
	if err != nil || result.Card == nil {
		t.Fatal(err)
	}
	view := result.Card.View
	if view.Page != 2 || view.FollowLatest || view.Anchor != selected.Card.View.Anchor || result.Card.Pages[view.Page-1].Content != want {
		t.Fatalf("reopen lost pinned selection: %#v", view)
	}
}

func TestPageSelectionFollowsAfterControllerReopen(t *testing.T) {
	ctx := context.Background()
	c, store, path, session := pageSelectionFixture(t)
	selectLatestPage(t, c, session.ID())
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardTypedHistory(ctx, session.ID(), "FOURTH"+strings.Repeat("y", 2890), "commentary"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	next := archiveController(t, reopened, nil, telegramcontroller.Options{Recovered: []domain.Session{session}, UIState: reopened})
	defer next.Close(ctx)
	result, err := next.ProjectCurrent(ctx, session.ID())
	if err != nil || result.Card == nil || result.Card.View.Page != 4 || !result.Card.View.FollowLatest {
		t.Fatalf("reopen lost follow: %#v %v", result, err)
	}
}

func TestPageSelectionPinnedAnchorSurvivesActualWindowShift(t *testing.T) {
	ctx := context.Background()
	c, store, _, session := pageSelectionFixture(t)
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for n := 4; n <= 32; n++ {
		if err := store.AppendCardTypedHistory(ctx, session.ID(), fmt.Sprintf("PAGE%02d", n)+strings.Repeat("x", 2890), "commentary"); err != nil {
			t.Fatal(err)
		}
	}
	preferencesStore, err := settings.OpenFileStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := preferencesStore.Update(ctx, func(current *settings.Settings) error {
		current.CardDetail, current.CardPageLimit = settings.CardDetailStandard, 32
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	preferences := settingscomposition.Preferences{Store: preferencesStore}
	c = archiveController(t, store, nil, telegramcontroller.Options{Recovered: []domain.Session{session}, UIState: store, Settings: preferences})
	defer c.Close(ctx)
	selectLatestPage(t, c, session.ID())
	selected, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticPagePrevious, SessionID: session.ID(), Page: 31})
	if err != nil || selected.Card == nil || selected.Card.View.Page != 31 {
		t.Fatalf("pin: %#v %v", selected, err)
	}
	want := selected.Card.Pages[30].Content
	if err := store.AppendCardTypedHistory(ctx, session.ID(), "PAGE33"+strings.Repeat("x", 2890), "commentary"); err != nil {
		t.Fatal(err)
	}
	result, err := c.ProjectCurrent(ctx, session.ID())
	if err != nil || result.Card == nil || result.Card.View.Page != 30 || result.Card.View.Anchor != selected.Card.View.Anchor || result.Card.Pages[29].Content != want || result.Card.View.FollowLatest {
		t.Fatalf("bounded window moved pinned content: %#v %v", result, err)
	}
}

func TestPageSelectionUsesNewDurableViewInsteadOfPreDeliveryCache(t *testing.T) {
	ctx := context.Background()
	c, store, _, session := pageSelectionFixture(t)
	latest := selectLatestPage(t, c, session.ID())
	// A confirmed delivery can select a new final-start page without invoking
	// controller navigation. Projection must read that committed durable view.
	if err := store.SetCardPage(ctx, session.ID(), 1, 3, latest.Pages[0].Anchors[0], false); err != nil {
		t.Fatal(err)
	}
	result, err := c.ProjectCurrent(ctx, session.ID())
	if err != nil || result.Card == nil || result.Card.View.Page != 1 || result.Card.View.FollowLatest {
		t.Fatalf("old controller cache overrode durable selection: %#v %v", result, err)
	}
}
