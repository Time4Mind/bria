package telegramui

import (
	"fmt"
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
		{
			Token: "two", Name: "web", NodeName: "must-not-render", Marker: "🟥",
			ContextPct: &percent, Selected: true,
		},
	})
	assertGoldenGrid(t, sessions, `[api ⏳ -> session@one] | [✓ web · 42% -> session@two]
[Servers -> sessions@servers] | [≡ Menu -> menu]`)
	archives := RenderArchives(ArchiveListInput{
		Copy: englishCopy, Title: "Build · archive", Page: 1, Pages: 1,
		Items: []ArchiveItem{{Token: "old", Name: "release", Index: 1}},
	})
	assertGoldenGrid(t, archives, `[1. release -> archive_item@old]
[1/1 -> noop]
[← Back -> menu]`)
}

func TestArchivePagesUseSameApprovedSixItemRichTable(t *testing.T) {
	items := make([]ArchiveItem, 0, 6)
	for index := 7; index <= 12; index++ {
		items = append(items, ArchiveItem{
			Token: OpaqueToken(fmt.Sprintf("s%d", index)), Name: fmt.Sprintf("session-%d", index),
			Description: []string{"Контекст сессии.", "Нужный результат."}, Index: index,
		})
	}
	screen := RenderArchives(ArchiveListInput{
		Copy: englishCopy, Title: "🗄 Archive · Mac", Items: items,
		Page: 2, Pages: 3, PreviousToken: "previous", NextToken: "next",
	})
	if strings.Contains(screen.Text, "2 of 3") || strings.Contains(screen.Text, "sessions") {
		t.Fatalf("archive text contains page or count metadata: %q", screen.Text)
	}
	want := "🗄 Archive · Mac\n\n⠀\n\n" +
		"| Name | Description |\n|---|---|\n" +
		"| 7. session-7 | · Контекст сессии.<br>· Нужный результат. |\n" +
		"| 8. session-8 | · Контекст сессии.<br>· Нужный результат. |\n" +
		"| 9. session-9 | · Контекст сессии.<br>· Нужный результат. |\n" +
		"| 10. session-10 | · Контекст сессии.<br>· Нужный результат. |\n" +
		"| 11. session-11 | · Контекст сессии.<br>· Нужный результат. |\n" +
		"| 12. session-12 | · Контекст сессии.<br>· Нужный результат. |"
	if screen.Text != want {
		t.Fatalf("archive text mismatch\n--- got ---\n%s\n--- want ---\n%s", screen.Text, want)
	}
	if !screen.RichMarkdown {
		t.Fatal("archive table is not marked as Rich Markdown")
	}
	if strings.Contains(screen.Text, "─────") || strings.Count(screen.Text, "<br>") != 6 {
		t.Fatalf("archive row layout is not the approved table: %q", screen.Text)
	}
	assertGoldenGrid(t, screen, `[7. session-7 -> archive_item@s7] | [8. session-8 -> archive_item@s8]
[9. session-9 -> archive_item@s9] | [10. session-10 -> archive_item@s10]
[11. session-11 -> archive_item@s11] | [12. session-12 -> archive_item@s12]
[◀ -> archive_prev@previous] | [2/3 -> noop] | [▶ -> archive_next@next]
[← Back -> menu]`)
}

func TestArchiveRichTableEscapesDynamicCells(t *testing.T) {
	screen := RenderArchives(ArchiveListInput{
		Copy: englishCopy, Title: "Archive", Page: 1, Pages: 1,
		Items: []ArchiveItem{{
			Token: "item", Index: 1, Name: "name | <x> *bold*",
			Description: []string{"one | <two>", "three\nfour"},
		}},
	})
	wantRow := "| 1. name \\| &lt;x&gt; \\*bold\\* | · one \\| &lt;two&gt;<br>· three four |"
	if !strings.Contains(screen.Text, wantRow) {
		t.Fatalf("escaped archive row=%q, want substring %q", screen.Text, wantRow)
	}
}

func TestEmptyArchiveRemainsPlainText(t *testing.T) {
	screen := RenderArchives(ArchiveListInput{
		Copy: englishCopy, Title: "Archive", Page: 1, Pages: 1,
	})
	if screen.RichMarkdown || screen.Text != "Archive" {
		t.Fatalf("empty archive=%#v", screen)
	}
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
			ContextPct: &percent, Selected: true,
		},
		{Token: "s3", Name: "train", NodeName: "GPU", Marker: "🟨", Status: "❓"},
		{Token: "s4", Name: "docs", NodeName: "Build", Marker: "🟧", Status: "❌"},
	})
	assertGoldenGrid(t, screen, `[🟦 api · Build ⏳ -> session@s1] | [✓ 🟩 web · Laptop · 37% -> session@s2] | [🟨 train · GPU ❓ -> session@s3]
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

func TestStartingOwnerCardCanCloseWithoutClear(t *testing.T) {
	screen := RenderSessionCard(CardInput{
		Text: "new session", Access: SharedControl, Owner: true, Starting: true,
		Tokens: cardTokens(),
	})
	assertGoldenGrid(t, screen, `[◀ -> page_prev@prev] | [1/1 -> page_latest@latest] | [▶ -> page_next@next]
[✖ Close -> close@close]
[+ new -> new] | [Servers -> sessions@servers] | [≡ Menu -> menu]`)
	assertActionAbsent(t, screen.Grid, ActionClear)
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
