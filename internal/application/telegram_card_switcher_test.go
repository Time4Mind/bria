package application_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestLiveCardCarriesThreePerRowModeAwareSessionSwitcher(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	actor := application.Principal{UserID: 2}
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	context := application.CardContext{BackgroundPercent: map[string]int{
		"alpha/a-old": 21, "alpha/a-new": 34, "alpha/shared": 55,
		"gamma/g-old": 8, "gamma/g-new": 13,
	}}
	hostFirst, err := projector.SessionCardPageWithContext(actor, ref, nil, 0, context)
	if err != nil {
		t.Fatal(err)
	}
	hostGrid := telegramui.CanonicalGrid(hostFirst.Grid)
	if !strings.Contains(hostGrid,
		"[a-old ✅ · 21% -> session@s-ao] | [✓ a-new ✅ · 34% -> session@s-an] | [shared ✅ · 55% -> session@s-as]") {
		t.Fatalf("host-first switcher=%s", hostGrid)
	}
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences
	allHosts, err := projector.SessionCardPageWithContext(actor, ref, nil, 0, context)
	if err != nil {
		t.Fatal(err)
	}
	allGrid := telegramui.CanonicalGrid(allHosts.Grid)
	if !strings.Contains(allGrid, "a-old · Alpha ✅") ||
		!strings.Contains(allGrid, "g-new · Gamma ✅ · 13%") ||
		!strings.Contains(allGrid, "✓ 🟥 a-new · Alpha ✅ · 34%") {
		t.Fatalf("all-host switcher=%s", allGrid)
	}
}

func TestLiveCardKeepsEveryReachableSessionInSwitcher(t *testing.T) {
	projector, state, tokens := projectorFixture(t)
	for index := 0; index < 12; index++ {
		id := domain.SessionID(fmt.Sprintf("many-%02d", index))
		ref := addProjectionSession(t, state, string(id), "alpha", 2, int64(100+index))
		tokens.sessions[ref.Key()] = telegramui.OpaqueToken(fmt.Sprintf("many-%02d", index))
	}
	state.Navigation.ActiveSessionByUserNode[2]["alpha"] = "many-11"
	screen, err := projector.SessionCard(application.Principal{UserID: 2},
		domain.SessionRef{NodeID: "alpha", SessionID: "many-11"})
	if err != nil {
		t.Fatal(err)
	}
	grid := telegramui.CanonicalGrid(screen.Grid)
	if strings.Count(grid, " -> session@") != 15 || !strings.Contains(grid, "many-11") ||
		!strings.Contains(grid, "a-old") {
		t.Fatalf("complete switcher=%s", grid)
	}
}

func TestLiveCardRemainsValidWithUnnamedStartingSessionAndHiddenEvents(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	starting := state.Sessions["alpha/a-old"]
	starting.Name = ""
	starting.RuntimePhase = domain.RuntimeStarting
	state.Sessions[starting.Ref().Key()] = starting
	preferences := state.Preferences[2]
	preferences.HiddenCardEvents = []domain.CardEventType{
		domain.CardEventToolCall, domain.CardEventToolResult,
	}
	preferences.TerminalSnapshots = domain.TerminalSnapshotAlways
	state.Preferences[2] = preferences

	screen, err := projector.SessionCard(application.Principal{UserID: 2},
		domain.SessionRef{NodeID: "alpha", SessionID: "a-new"})
	if err != nil {
		t.Fatal(err)
	}
	if err := screen.Validate(); err != nil {
		t.Fatalf("card settings must never produce an invalid screen: %v\n%s", err,
			telegramui.CanonicalGrid(screen.Grid))
	}
	if !strings.Contains(telegramui.CanonicalGrid(screen.Grid), "[… ⏳ -> session@s-ao]") {
		t.Fatalf("unnamed session did not get a safe label: %s", telegramui.CanonicalGrid(screen.Grid))
	}
}
