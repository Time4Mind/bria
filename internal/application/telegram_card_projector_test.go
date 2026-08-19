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
	if screen.PaneAnchorOffset <= 0 ||
		!strings.HasSuffix(screen.Text[:screen.PaneAnchorOffset], "answer") ||
		strings.Contains(screen.Text[:screen.PaneAnchorOffset], "context:") ||
		!strings.Contains(screen.Text[screen.PaneAnchorOffset:], "context: 24%") ||
		!strings.Contains(screen.Text[screen.PaneAnchorOffset:], "─── background ───") {
		t.Fatalf("terminal anchor does not precede context/background: offset=%d text=%q",
			screen.PaneAnchorOffset, screen.Text)
	}
}

func TestSessionCardAnchorSurvivesLeadingPageEviction(t *testing.T) {
	projector, _, _ := projectorFixture(t)
	actor := application.Principal{UserID: 2}
	ref := domain.SessionRef{NodeID: "alpha", SessionID: "a-new"}
	events := make([]application.CardEvent, 0, 6)
	for index := 1; index <= 6; index++ {
		events = append(events, application.CardEvent{
			ID: fmt.Sprintf("event-%d", index), Kind: application.CardEventAssistantText,
			Text:      fmt.Sprintf("answer-%d %s", index, strings.Repeat("content ", 180)),
			PageBreak: true,
		})
	}
	initial := application.RenderCardEventPages(
		domain.DefaultUserPreferences(), events, application.CardRenderOptions{},
	)
	target := initial.Pages[3]
	if target.Anchor == "" || target.Number != 4 {
		t.Fatalf("initial target=%#v", target)
	}
	screen, err := projector.SessionCardViewWithContext(
		actor, ref, events[2:], target.Number, target.Anchor, application.CardContext{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if screen.Grid[0][1].Label != "2/4" || !strings.Contains(screen.Text, "answer-4") ||
		screen.Checkpoint == nil || screen.Checkpoint.PageAnchor != target.Anchor {
		t.Fatalf("anchored page did not follow content after eviction: %#v", screen)
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
