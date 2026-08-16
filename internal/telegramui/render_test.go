package telegramui

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

var englishCopy = i18n.For("en")

func TestMainMenuGolden(t *testing.T) {
	screen := RenderMainMenu(englishCopy, "backend")
	assertGoldenGrid(t, screen, `[📋 Sessions -> sessions] | [🗄 Archive -> archive]
[📊 Status -> status] | [🆕 New -> new]
[⚙ Settings -> settings]`)
}

func TestHostFirstNodePickerGolden(t *testing.T) {
	screen := RenderHostFirstNodes(englishCopy, []NodeItem{
		{
			Token:        "node-local",
			Name:         "Laptop",
			Status:       NodeOnline,
			LiveSessions: 2,
			Selected:     true,
		},
		{
			Token:        "node-build",
			Name:         "Build server",
			Status:       NodeReconnecting,
			LiveSessions: 1,
		},
		{
			Token:  "node-gpu",
			Name:   "GPU server",
			Status: NodeOffline,
		},
	})
	assertGoldenGrid(t, screen, `[🟢 ✓ Laptop (2) -> node@node-local]
[🟡 Build server (1) -> node@node-build]
[🔴 GPU server · unavailable -> node@node-gpu]
[← Back -> sessions]`)
}

func TestNodeSessionAndArchiveSurfacesGolden(t *testing.T) {
	percent := 42
	sessions := RenderNodeSessions(englishCopy, NodeItem{Name: "Build", Status: NodeOnline}, []SessionItem{
		{Token: "one", Name: "api", Status: "⏳"},
		{Token: "two", Name: "web", Status: "🟢", ContextPct: &percent},
	})
	assertGoldenGrid(t, sessions, `[api ⏳ -> session@one] | [web 🟢 · 42% -> session@two]
[Servers -> sessions@servers] | [≡ Menu -> menu]`)
	archives := RenderArchives(ArchiveListInput{
		Copy: englishCopy, Title: "Build · archive", Page: 1, Pages: 1, Total: 1,
		Items: []ArchiveItem{{Token: "old", Name: "release", Index: 1}},
	})
	assertGoldenGrid(t, archives, `[1. release -> archive_item@old]
[1/1 -> noop]
[← Back -> menu]`)
}

func TestUnavailableNodeKeepsReadSurfacesGolden(t *testing.T) {
	last := SessionItem{Token: "last", Name: "backend"}
	screen := RenderUnavailableNode(
		englishCopy,
		NodeItem{Name: "Build", Status: NodeOffline},
		&last,
	)
	assertGoldenGrid(t, screen, `[Last card · backend -> session@last]
[🗄 Archive -> archive]
[← Servers -> sessions@servers]`)
}

func TestNodeIsolationControlIsAdministrative(t *testing.T) {
	member := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "node", Name: "Node"},
	})
	assertActionAbsent(t, member.Grid, ActionNodeIsolationRequire)
	admin := RenderNodeMembership(NodeMembershipInput{
		Copy: englishCopy, Node: domain.Node{ID: "node", Name: "Node"},
		CanManageIsolation: true, IsolationCanRequire: true, IsolationRequireToken: "require",
	})
	if !strings.Contains(CanonicalGrid(admin.Grid), "Require isolation") {
		t.Fatalf("administrative isolation control missing: %s", CanonicalGrid(admin.Grid))
	}
}

func TestAllHostSessionsUseThreeButtonsPerRowGolden(t *testing.T) {
	percent := 37
	screen := RenderAllHostSessions(englishCopy, []SessionItem{
		{Token: "s1", Name: "api", NodeName: "Build", Marker: "🟦", Status: "⏳"},
		{
			Token: "s2", Name: "web", NodeName: "Laptop", Marker: "🟩",
			Status: "🟢", ContextPct: &percent,
		},
		{Token: "s3", Name: "train", NodeName: "GPU", Marker: "🟨", Status: "❓"},
		{Token: "s4", Name: "docs", NodeName: "Build", Marker: "🟧", Status: "❌"},
	})
	assertGoldenGrid(t, screen, `[🟦 api · Build ⏳ -> session@s1] | [🟩 web · Laptop 🟢 · 37% -> session@s2] | [🟨 train · GPU ❓ -> session@s3]
[🟧 docs · Build ❌ -> session@s4]
[🆕 New -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
	if got := len(screen.Grid[0]); got != sessionsPerRow {
		t.Fatalf("first session row has %d buttons, want %d", got, sessionsPerRow)
	}
}

func TestSharedViewCardIsReadOnlyGolden(t *testing.T) {
	screen := RenderSessionCard(CardInput{
		Text:   "api · Build\nclaude · active",
		Access: SharedView,
		Page:   2,
		Pages:  4,
		Tokens: cardTokens(),
	})
	assertGoldenGrid(t, screen, `[◀ -> page_prev@prev] | [2/4 -> page_latest@latest] | [▶ -> page_next@next]
[+ new -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
	if want := "👁 View only"; !strings.Contains(screen.Text, want) {
		t.Fatalf("view-only card text %q does not contain %q", screen.Text, want)
	}
	for _, action := range []Action{ActionStop, ActionClose, ActionClear, ActionTerminal} {
		assertActionAbsent(t, screen.Grid, action)
	}
}

func TestSharedControlCardPreservesCCBotControlsGolden(t *testing.T) {
	screen := RenderSessionCard(CardInput{
		Text:            "api · Build\nclaude · active",
		Access:          SharedControl,
		Owner:           true,
		Busy:            true,
		Page:            1,
		Pages:           1,
		CanOpenTerminal: true,
		Tokens:          cardTokens(),
	})
	assertGoldenGrid(t, screen, `[◀ -> page_prev@prev] | [1/1 -> page_latest@latest] | [▶ -> page_next@next]
[⏹ Stop -> stop@stop] | [🧹 Clear -> clear@clear] | [🖥 Term -> terminal@term]
[+ new -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
}

func TestIdleOwnerCardUsesCloseButtonGolden(t *testing.T) {
	screen := RenderSessionCard(CardInput{
		Text:   "api · Build\nclaude · idle",
		Access: SharedControl,
		Owner:  true,
		Tokens: cardTokens(),
	})
	assertGoldenGrid(t, screen, `[◀ -> page_prev@prev] | [1/1 -> page_latest@latest] | [▶ -> page_next@next]
[✖ Close -> close@close] | [🧹 Clear -> clear@clear]
[+ new -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
}

func TestIdleSharedControllerHasNoLifecycleControls(t *testing.T) {
	screen := RenderSessionCard(CardInput{
		Text: "shared", Access: SharedControl, Tokens: cardTokens(),
	})
	assertGoldenGrid(t, screen, `[◀ -> page_prev@prev] | [1/1 -> page_latest@latest] | [▶ -> page_next@next]
[+ new -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
}

func TestUnknownCardAccessFailsClosed(t *testing.T) {
	screen := RenderSessionCard(CardInput{Text: "private session"})
	for _, action := range []Action{ActionStop, ActionClose, ActionClear, ActionTerminal} {
		assertActionAbsent(t, screen.Grid, action)
	}
}

func TestReadyArchiveOffersRestoreWithoutClaimingViewOnly(t *testing.T) {
	screen := RenderSessionCard(CardInput{
		Text: "archived", Access: SharedView, Owner: true, CanRestore: true,
		Tokens: cardTokens(),
	})
	assertGoldenGrid(t, screen, `[◀ -> page_prev@prev] | [1/1 -> page_latest@latest] | [▶ -> page_next@next]
[↻ Restore -> restore@restore]
[+ new -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
	if strings.Contains(screen.Text, "View only") {
		t.Fatalf("restorable archive text=%q", screen.Text)
	}
}

func cardTokens() map[Action]OpaqueToken {
	return map[Action]OpaqueToken{
		ActionPagePrevious: "prev", ActionPageLatest: "latest",
		ActionPageNext: "next", ActionStop: "stop",
		ActionClose: "close", ActionClear: "clear", ActionTerminal: "term",
		ActionRestore: "restore",
	}
}

func assertGoldenGrid(t *testing.T, screen Screen, want string) {
	t.Helper()
	if err := screen.Validate(); err != nil {
		t.Fatalf("screen does not validate: %v", err)
	}
	if got := CanonicalGrid(screen.Grid); got != want {
		t.Fatalf("grid mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func assertActionAbsent(t *testing.T, grid Grid, action Action) {
	t.Helper()
	for _, row := range grid {
		for _, item := range row {
			if item.Callback.Action == action {
				t.Fatalf("read-only grid unexpectedly contains action %q", action)
			}
		}
	}
}
