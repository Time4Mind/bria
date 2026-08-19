package telegramapp

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestClusterUpdateCardMarkerIsDurableAndClearsOnNavigation(t *testing.T) {
	handler := &Handler{}
	message := telegrambot.Message{PaneHash: "old-pane"}
	update := telegramui.Screen{Grid: telegramui.Grid{telegramui.Row{{
		Label: "refresh", Callback: telegramui.Callback{Action: telegramui.ActionClusterUpdateRefresh},
	}}}}
	if got := handler.responseCardPaneHash(7, message, update); got != clusterUpdateCardMarker {
		t.Fatalf("update marker=%q", got)
	}
	menu := telegramui.Screen{Grid: telegramui.Grid{telegramui.Row{{
		Label: "menu", Callback: telegramui.Callback{Action: telegramui.ActionMenu},
	}}}}
	if got := handler.responseCardPaneHash(7, telegrambot.Message{}, menu); got != "" {
		t.Fatalf("navigation retained update marker=%q", got)
	}
}

func TestSessionPageMarkerPersistsViewWithoutLeakingIntoTransport(t *testing.T) {
	handler := &Handler{sessionPages: make(map[sessionPageKey]cardPageState)}
	message := telegrambot.Message{PaneHash: "pane-v1"}
	screen := telegramui.Screen{
		Name: telegramui.ScreenSessionCard,
		Checkpoint: &telegramui.SessionCheckpoint{
			NodeID: "node-1", SessionID: "session-1", PageAnchor: "anchor.0",
		},
		Grid: telegramui.Grid{telegramui.Row{
			{Label: "previous"},
			{Label: "18/20", Callback: telegramui.Callback{Action: telegramui.ActionPageLatest}},
		}},
	}
	ref := domain.SessionRef{NodeID: "node-1", SessionID: "session-1"}
	handler.sessionPages[pageKey(7, ref)] = cardPageState{
		page: 18, pages: 20, anchor: "anchor.0", follow: false,
	}
	encoded := handler.responseCardPaneHash(7, message, screen)
	state, paneHash, ok := decodeSessionPagePaneHash(encoded)
	if !ok || state.page != 18 || state.pages != 20 || state.follow ||
		state.anchor != "anchor.0" || paneHash != message.PaneHash {
		t.Fatalf("decoded page marker = %#v %q ok=%v", state, paneHash, ok)
	}
	transport := telegramMessage(domain.TelegramResponseCard{PaneHash: encoded})
	if transport.PaneHash != message.PaneHash {
		t.Fatalf("transport pane hash = %q", transport.PaneHash)
	}
}

func TestResolvedLastPageDoesNotInventFollowIntent(t *testing.T) {
	handler := &Handler{sessionPages: make(map[sessionPageKey]cardPageState)}
	ref := domain.SessionRef{NodeID: "node-1", SessionID: "session-1"}
	screen := telegramui.Screen{
		Name: telegramui.ScreenSessionCard,
		Checkpoint: &telegramui.SessionCheckpoint{
			NodeID: "node-1", SessionID: "session-1", PageAnchor: "chunk.0",
		},
		Grid: telegramui.Grid{telegramui.Row{
			{Label: "previous"},
			{Label: "2/2", Callback: telegramui.Callback{Action: telegramui.ActionPageLatest}},
		}},
	}
	handler.rememberResolvedCardPageWithFollow(7, ref, screen, false)
	// Background renders update the resolved page count but must preserve the
	// user's explicit pin even when that chunk temporarily becomes the last page.
	handler.rememberResolvedCardPage(7, ref, screen)
	state, ok := handler.cardPageState(7, ref)
	if !ok || state.follow || state.anchor != "chunk.0" || handler.rememberedCardPage(7, ref) != 2 {
		t.Fatalf("pinned last page became follow state: %#v ok=%v", state, ok)
	}
}

func TestInputOnLastPageRestoresFollowIntent(t *testing.T) {
	handler := &Handler{sessionPages: make(map[sessionPageKey]cardPageState)}
	ref := domain.SessionRef{NodeID: "node-1", SessionID: "session-1"}
	key := pageKey(7, ref)
	handler.sessionPages[key] = cardPageState{
		page: 12, pages: 12, anchor: "last-chunk.0", follow: false,
	}
	handler.restoreFollowForInput(7, ref)
	state := handler.sessionPages[key]
	if !state.follow || state.anchor != "" || state.page != 12 || state.pages != 12 {
		t.Fatalf("input did not restore follow: %#v", state)
	}
}

func TestInputOnHistoricalPageKeepsPinnedIntent(t *testing.T) {
	handler := &Handler{sessionPages: make(map[sessionPageKey]cardPageState)}
	ref := domain.SessionRef{NodeID: "node-1", SessionID: "session-1"}
	key := pageKey(7, ref)
	want := cardPageState{page: 11, pages: 12, anchor: "older-chunk.0", follow: false}
	handler.sessionPages[key] = want
	handler.restoreFollowForInput(7, ref)
	if got := handler.sessionPages[key]; got != want {
		t.Fatalf("historical input changed pin: got=%#v want=%#v", got, want)
	}
}
