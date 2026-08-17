package application_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestSessionCardUsesSingleLineHeader(t *testing.T) {
	projector, _, _ := projectorFixture(t)
	screen, err := projector.SessionCard(application.Principal{UserID: 2},
		domain.SessionRef{NodeID: "alpha", SessionID: "a-new"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "a-new · Alpha · claude · —") {
		t.Fatalf("session header is not a single line: %q", screen.Text)
	}
	if !strings.HasPrefix(screen.Text, "a-new · Alpha · claude · —\n\n─────") {
		t.Fatalf("header separator does not match CCBot Markdown layout: %q", screen.Text)
	}
}

func TestSessionCardSeparatesContextAndBackgroundLikeCCBot(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	backgroundRef := domain.SessionRef{NodeID: "alpha", SessionID: "a-old"}
	state.Navigation.BackgroundByUser[2] = map[string]domain.BackgroundNotice{
		backgroundRef.Key(): {
			Session: backgroundRef, Kind: domain.BackgroundFinished,
			EventRevision: 2, ChangedAt: time.Unix(80, 0).UTC(),
		},
	}
	activePercent := 24
	screen, err := projector.SessionCardPageWithContext(
		application.Principal{UserID: 2}, ref,
		[]application.CardEvent{{Kind: application.CardEventAssistantText, Text: "answer"}},
		1, application.CardContext{
			ActivePercent:     &activePercent,
			BackgroundPercent: map[string]int{backgroundRef.Key(): 61},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n\nanswer\n\n\u00a0\n\ncontext: 24%\n\n\u00a0\n\n─── background ───  \na-old ✅ · 61%"
	if !strings.Contains(screen.Text, want) {
		t.Fatalf("card metadata layout differs from CCBot:\n%q\nwant tail:\n%q", screen.Text, want)
	}
}

func TestSessionCardUsesAgentTimestampAsRecordedByBackend(t *testing.T) {
	projector, _, _ := projectorFixture(t)
	screen, err := projector.SessionCardPage(
		application.Principal{UserID: 2},
		domain.SessionRef{NodeID: "alpha", SessionID: "a-new"},
		[]application.CardEvent{{
			Kind: application.CardEventAssistantText,
			Text: "done", StartedAt: time.Date(2026, 8, 13, 10, 44, 13, 0, time.FixedZone("MSK", 3*60*60)),
		}}, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "a-new · Alpha · claude · 10:44:13") {
		t.Fatalf("session header changed the backend activity timezone: %q", screen.Text)
	}
}

func TestFailedStopKeepsStopInsteadOfShowingClose(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	session := state.Sessions[ref.Key()]
	session.RuntimePhase = domain.RuntimeDegraded
	session.LastOperation = &domain.SessionOperationResult{
		OperationID: "stop-failed", Action: domain.ActionStop,
		Status: domain.OperationFailed, At: time.Unix(50, 0).UTC(),
	}
	state.Sessions[ref.Key()] = session
	screen, err := projector.SessionCard(application.Principal{UserID: 2}, ref)
	if err != nil {
		t.Fatal(err)
	}
	grid := telegramui.CanonicalGrid(screen.Grid)
	if !strings.Contains(grid, "-> stop@") || strings.Contains(grid, "-> close@") {
		t.Fatalf("degraded card controls=%q", grid)
	}
}

func TestFailedInputNoticeFollowsTranscriptOnLatestPage(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	session := state.Sessions[ref.Key()]
	session.LastOperation = &domain.SessionOperationResult{
		OperationID: "input-failed", Action: domain.ActionSendInput,
		Status: domain.OperationFailed, At: time.Unix(50, 0).UTC(),
	}
	state.Sessions[ref.Key()] = session
	screen, err := projector.SessionCardPage(
		application.Principal{UserID: 2}, ref,
		[]application.CardEvent{{
			Kind: application.CardEventAssistantText, Text: "earlier transcript event",
		}}, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	eventAt := strings.Index(screen.Text, "earlier transcript event")
	failureAt := strings.Index(screen.Text, "The message could not be delivered")
	if eventAt < 0 || failureAt <= eventAt {
		t.Fatalf("failed-input notice is not chronological: %q", screen.Text)
	}
}

func TestReconnectingNodeCardIsReadOnlyAndMarkedUnavailable(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	node := state.Nodes["alpha"]
	node.Status = domain.NodeReconnecting
	state.Nodes["alpha"] = node
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	screen, err := projector.SessionCard(application.Principal{UserID: 2}, ref)
	if err != nil {
		t.Fatal(err)
	}
	grid := telegramui.CanonicalGrid(screen.Grid)
	if !strings.Contains(screen.Text, "Server unavailable") ||
		strings.Contains(grid, "-> stop@") || strings.Contains(grid, "-> close@") ||
		strings.Contains(grid, "-> clear@") {
		t.Fatalf("reconnecting card=%q grid=%s", screen.Text, grid)
	}
}

func TestOfflineSessionCardShowsReplicatedQueueWithoutViewOnlyLabel(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	node := state.Nodes[ref.NodeID]
	node.Status = domain.NodeOffline
	state.Nodes[ref.NodeID] = node
	session := state.Sessions[ref.Key()]
	state.DeferredInputs[ref.Key()] = []domain.DeferredSessionInput{{
		OperationID: "queued-1", ActorID: 2, Session: ref,
		ExpectedGeneration: session.RuntimeGeneration, Kind: domain.DeferredInputText,
		Text: "hello", QueuedAt: time.Unix(100, 0).UTC(),
	}}
	screen, err := projector.SessionCard(application.Principal{UserID: 2}, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(screen.Text, "Waiting for node recovery, request queued · 1/5") ||
		strings.Contains(screen.Text, "View only") {
		t.Fatalf("offline queued card=%q", screen.Text)
	}
}

func TestBackgroundPanelFollowsSessionViewMode(t *testing.T) {
	projector, state, _ := projectorFixture(t)
	state.Navigation.BackgroundByUser[2] = map[string]domain.BackgroundNotice{
		"alpha/a-old": {
			Session: domain.SessionRef{NodeID: "alpha", SessionID: "a-old"},
			Kind:    domain.BackgroundFinished, EventRevision: 2, ChangedAt: time.Unix(80, 0).UTC(),
		},
		"gamma/g-new": {
			Session: domain.SessionRef{NodeID: "gamma", SessionID: "g-new"},
			Kind:    domain.BackgroundError, EventRevision: 2, ChangedAt: time.Unix(90, 0).UTC(),
		},
		"gamma/g-old": {
			Session: domain.SessionRef{NodeID: "gamma", SessionID: "g-old"},
			Kind:    domain.BackgroundWorking, EventRevision: 2, ChangedAt: time.Unix(95, 0).UTC(),
			Dismissed: true,
		},
	}
	actor := application.Principal{UserID: 2}
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	hostFirst, err := projector.SessionCardPageWithContext(
		actor, ref, nil, 0,
		application.CardContext{BackgroundPercent: map[string]int{"alpha/a-old": 37}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hostFirst.Text, "a-old ✅ · 37%") || strings.Contains(hostFirst.Text, "g-new") ||
		strings.Contains(hostFirst.Text, "g-old") {
		t.Fatalf("host-first panel=%q", hostFirst.Text)
	}
	preferences := state.Preferences[2]
	preferences.SessionView = domain.ViewAllHosts
	state.Preferences[2] = preferences
	allHosts, err := projector.SessionCardPageWithContext(
		actor, ref, nil, 0,
		application.CardContext{BackgroundPercent: map[string]int{
			"alpha/a-old": 37, "gamma/g-new": 82,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(allHosts.Text, "g-new · Gamma ❌ · 82%") ||
		!strings.Contains(allHosts.Text, "a-old · Alpha ✅ · 37%") ||
		strings.Contains(allHosts.Text, "g-old") {
		t.Fatalf("all-host panel=%q", allHosts.Text)
	}
}

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
		"[a-old · 21% -> session@s-ao] | [✓ a-new · 34% -> session@s-an] | [shared · 55% -> session@s-as]") {
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
	if !strings.Contains(allGrid, "a-old · Alpha") ||
		!strings.Contains(allGrid, "g-new · Gamma · 13%") ||
		!strings.Contains(allGrid, "✓ 🟥 a-new · Alpha · 34%") {
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

func TestEveryCardDisplayCombinationPreservesValidSessionNavigation(t *testing.T) {
	cardModes := []domain.ResponseCardMode{
		domain.ResponseCardsKeepPaginated, domain.ResponseCardsKeepLatest,
		domain.ResponseCardsReplace,
	}
	terminalModes := []domain.TerminalSnapshotMode{
		domain.TerminalSnapshotWorking, domain.TerminalSnapshotAlways,
		domain.TerminalSnapshotNever,
	}
	hiddenSets := [][]domain.CardEventType{
		nil,
		{domain.CardEventToolCall},
		{domain.CardEventToolResult},
		{domain.CardEventToolCall, domain.CardEventToolResult, domain.CardEventThinking},
	}
	for _, cardMode := range cardModes {
		for _, terminalMode := range terminalModes {
			for _, hidden := range hiddenSets {
				projector, state, _ := projectorFixture(t)
				unnamed := state.Sessions["alpha/a-old"]
				unnamed.Name = ""
				state.Sessions[unnamed.Ref().Key()] = unnamed
				preferences := state.Preferences[2]
				preferences.ResponseCards = cardMode
				preferences.TerminalSnapshots = terminalMode
				preferences.HiddenCardEvents = hidden
				state.Preferences[2] = preferences
				screen, err := projector.SessionCard(application.Principal{UserID: 2},
					domain.SessionRef{NodeID: "alpha", SessionID: "a-new"})
				if err != nil {
					t.Fatalf("%s/%s/%v: %v", cardMode, terminalMode, hidden, err)
				}
				if err := screen.Validate(); err != nil {
					t.Fatalf("%s/%s/%v: invalid card: %v", cardMode, terminalMode, hidden, err)
				}
				if !strings.Contains(telegramui.CanonicalGrid(screen.Grid), "[… -> session@s-ao]") {
					t.Fatalf("%s/%s/%v: session switcher disappeared", cardMode, terminalMode, hidden)
				}
			}
		}
	}
}
