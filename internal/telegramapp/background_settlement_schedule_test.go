package telegramapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type countingTranscriptControls struct {
	SessionControls
	calls  int
	events []transcript.Event
	err    error
}

func TestBackgroundSettlementReadFailureUsesShortRetry(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	session := domain.Session{
		ID: "background", NodeID: "node", OwnerID: 7, Name: "Background",
		Backend: "codex", ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeRunning, RuntimeGeneration: 3,
		CreatedAt: now.Add(-time.Minute), LiveSinceAt: now.Add(-time.Minute),
		LastEventAt: now.Add(-time.Second),
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	port := &clusterLogPort{state: state}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	controls := &countingTranscriptControls{err: errors.New("unavailable")}
	handler := &Handler{
		service: service, controls: controls,
		paneRefreshState:   newPaneRefreshState(),
		cardRuntimeState:   newCardRuntimeState(),
		transcriptTriggers: newTranscriptTriggerTracker(now),
	}
	schedule := make(backgroundSettlementSchedule)
	retry := 1200 * time.Millisecond
	handler.settleDueRunningSessions(context.Background(), now, retry, schedule)
	if controls.calls != 1 {
		t.Fatalf("initial reads=%d", controls.calls)
	}
	controls.err = nil
	handler.settleDueRunningSessions(context.Background(), now.Add(time.Second), retry, schedule)
	if controls.calls != 1 {
		t.Fatalf("failure retried too early: calls=%d", controls.calls)
	}
	handler.settleDueRunningSessions(
		context.Background(), now.Add(retry+50*time.Millisecond), retry, schedule,
	)
	if controls.calls != 2 {
		t.Fatalf("failure did not retry after %s: calls=%d", retry, controls.calls)
	}
}

func TestBackgroundSettlementIntervalStartsAfterReadCompletes(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(7 * time.Second)
	next := nextBackgroundSettlement(backgroundSettlementResult{
		readOK: true, completed: completedAt,
	}, startedAt, 1200*time.Millisecond)
	if want := completedAt.Add(backgroundSettlementFallback); !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
}

func (controls *countingTranscriptControls) Transcript(
	context.Context,
	application.Principal,
	domain.SessionRef,
) ([]transcript.Event, error) {
	controls.calls++
	return cloneTranscriptEvents(controls.events), controls.err
}

func TestBackgroundSettlementSkipsOnlyExactActivePaneWorker(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	session := domain.Session{
		ID: "active", NodeID: "node", OwnerID: 7, Name: "Active",
		Backend: "codex", ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeRunning, RuntimeGeneration: 3,
		CreatedAt: now.Add(-time.Minute), LiveSinceAt: now.Add(-time.Minute),
		LastEventAt: now.Add(-time.Second),
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(7, session.Ref(), now); err != nil {
		t.Fatal(err)
	}
	port := &clusterLogPort{state: state}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	controls := &countingTranscriptControls{events: []transcript.Event{{
		Kind: transcript.EventToolCall, Head: "Bash", Timestamp: now.Format(time.RFC3339Nano),
	}}}
	handler := &Handler{
		service: service, controls: controls,
		paneRefreshState:   newPaneRefreshState(),
		cardRuntimeState:   newCardRuntimeState(),
		transcriptTriggers: newTranscriptTriggerTracker(now),
	}
	actor := application.Principal{UserID: 7}
	handler.paneWorkers[actor.UserID] = 4
	handler.paneWorkerRefs[actor.UserID] = session.Ref()
	schedule := make(backgroundSettlementSchedule)

	handler.settleDueRunningSessions(context.Background(), now, 1200*time.Millisecond, schedule)
	if controls.calls != 0 {
		t.Fatalf("exact active worker caused %d duplicate transcript reads", controls.calls)
	}

	handler.paneWorkerRefs[actor.UserID] = domain.SessionRef{NodeID: "node", SessionID: "stale"}
	handler.settleDueRunningSessions(context.Background(), now, 1200*time.Millisecond, schedule)
	if controls.calls != 1 {
		t.Fatalf("stale worker suppressed watchdog: calls=%d", controls.calls)
	}
	handler.settleDueRunningSessions(
		context.Background(), now.Add(time.Second), 1200*time.Millisecond, schedule,
	)
	if controls.calls != 1 {
		t.Fatalf("unchanged transcript was rescanned before fallback: calls=%d", controls.calls)
	}
	handler.settleDueRunningSessions(
		context.Background(), now.Add(backgroundSettlementFallback+50*time.Millisecond),
		1200*time.Millisecond, schedule,
	)
	if controls.calls != 2 {
		t.Fatalf("fallback did not rescan after %s: calls=%d",
			backgroundSettlementFallback, controls.calls)
	}
}

func TestActiveFinalReconciliationUsesFiveSecondWatchdog(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	session := domain.Session{
		ID: "active-idle", NodeID: "node", OwnerID: 7, Name: "Active idle",
		Backend: "codex", ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 3,
		CreatedAt: now.Add(-time.Minute), LiveSinceAt: now.Add(-time.Minute),
		LastEventAt: now.Add(-time.Second),
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	if err := state.SelectSession(7, session.Ref(), now); err != nil {
		t.Fatal(err)
	}
	port := &clusterLogPort{state: state}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	codec, err := callbacktoken.New([]byte(strings.Repeat("k", callbacktoken.KeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	projector, err := application.NewTelegramProjector(port, codec)
	if err != nil {
		t.Fatal(err)
	}
	controls := &countingTranscriptControls{}
	handler, err := NewHandlerWithControls(
		service, projector, codec, &clusterLogMessenger{}, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	schedule := make(activeFinalReconcileSchedule)
	handler.reconcileActiveFinalCards(context.Background(), now, schedule)
	if controls.calls != 1 {
		t.Fatalf("initial reconciliation reads=%d", controls.calls)
	}
	handler.reconcileActiveFinalCards(
		context.Background(), now.Add(1200*time.Millisecond), schedule,
	)
	if controls.calls != 1 {
		t.Fatalf("transcript rescanned at hot-loop cadence: calls=%d", controls.calls)
	}
	handler.reconcileActiveFinalCards(
		context.Background(), now.Add(activeFinalReconcileFallback+50*time.Millisecond), schedule,
	)
	if controls.calls != 2 {
		t.Fatalf("watchdog did not rescan after %s: calls=%d",
			activeFinalReconcileFallback, controls.calls)
	}
}
