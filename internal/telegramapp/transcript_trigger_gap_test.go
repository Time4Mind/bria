package telegramapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

func triggerGapTurn(
	promptAt time.Time,
	answer string,
) (domain.Session, []transcript.Event) {
	finalAt := promptAt.Add(2 * time.Second)
	session := domain.Session{
		ID: "session", NodeID: "node", Backend: "codex",
		ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeRunning, RuntimeGeneration: 3,
		LastEventAt: promptAt,
		LastOperation: &domain.SessionOperationResult{
			OperationID: "input", Action: domain.ActionSendInput,
			Status: domain.OperationQueued, At: promptAt,
		},
	}
	events := []transcript.Event{
		{Kind: transcript.EventUserText, Text: "prompt", Timestamp: promptAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventToolCall, ToolName: "Read", ToolUseID: "tool-1",
			Timestamp: promptAt.Add(time.Second).Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: answer,
			Timestamp: finalAt.Format(time.RFC3339Nano)},
	}
	return session, events
}

func TestTranscriptTriggerGapIsReportedOnceAfterGrace(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	promptAt := startedAt.Add(time.Second)
	session, events := triggerGapTurn(promptAt, "final")
	tracker := newTranscriptTriggerTracker(startedAt)
	detectedAt := promptAt.Add(3 * time.Second)

	tracker.observeWatchdog(session, events, "live_card", detectedAt)
	if gaps := tracker.flushDue(detectedAt.Add(transcriptTriggerGrace - time.Millisecond)); len(gaps) != 0 {
		t.Fatalf("gap escaped grace period: %#v", gaps)
	}
	gaps := tracker.flushDue(detectedAt.Add(transcriptTriggerGrace))
	if len(gaps) != 1 {
		t.Fatalf("gaps=%d want=1", len(gaps))
	}
	gap := gaps[0]
	if gap.ref != session.Ref() || gap.generation != session.RuntimeGeneration ||
		gap.source != "live_card" || gap.missingEvents != 3 || gap.deltaComplete ||
		!strings.Contains(gap.kinds, "assistant_final:1") ||
		!strings.Contains(gap.kinds, "tool_call:1") ||
		!strings.Contains(gap.kinds, "user_text:1") {
		t.Fatalf("gap=%#v", gap)
	}
	if repeated := tracker.flushDue(detectedAt.Add(2 * transcriptTriggerGrace)); len(repeated) != 0 {
		t.Fatalf("gap was reported twice: %#v", repeated)
	}
}

func TestTranscriptTriggerConfirmationWithinGraceCancelsGap(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	promptAt := startedAt.Add(time.Second)
	session, events := triggerGapTurn(promptAt, "final")
	tracker := newTranscriptTriggerTracker(startedAt)
	detectedAt := promptAt.Add(3 * time.Second)

	tracker.observeWatchdog(session, events, "background", detectedAt)
	tracker.confirm(session, events, detectedAt.Add(time.Second))
	if gaps := tracker.flushDue(detectedAt.Add(transcriptTriggerGrace)); len(gaps) != 0 {
		t.Fatalf("confirmed trigger produced gap: %#v", gaps)
	}
}

func TestTranscriptTriggerGapReportsOnlyEventsAfterTriggerSnapshot(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	promptAt := startedAt.Add(time.Second)
	session, events := triggerGapTurn(promptAt, "final")
	tracker := newTranscriptTriggerTracker(startedAt)
	detectedAt := promptAt.Add(3 * time.Second)

	tracker.confirm(session, events[:2], promptAt.Add(time.Second))
	tracker.observeWatchdog(session, events, "background", detectedAt)
	gaps := tracker.flushDue(detectedAt.Add(transcriptTriggerGrace))
	if len(gaps) != 1 {
		t.Fatalf("gaps=%d want=1", len(gaps))
	}
	if gaps[0].missingEvents != 1 || gaps[0].kinds != "assistant_final:1" ||
		!gaps[0].deltaComplete {
		t.Fatalf("gap delta=%#v", gaps[0])
	}
}

func TestTranscriptTriggerStalePriorFinalDoesNotMaskNewGap(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tracker := newTranscriptTriggerTracker(startedAt)
	oldSession, oldEvents := triggerGapTurn(startedAt.Add(time.Second), "old")
	tracker.confirm(oldSession, oldEvents, startedAt.Add(4*time.Second))

	newSession, newEvents := triggerGapTurn(startedAt.Add(10*time.Second), "new")
	detectedAt := startedAt.Add(13 * time.Second)
	tracker.observeWatchdog(newSession, newEvents, "live_card", detectedAt)
	if gaps := tracker.flushDue(detectedAt.Add(transcriptTriggerGrace)); len(gaps) != 1 {
		t.Fatalf("new final was masked by stale confirmation: gaps=%d", len(gaps))
	}
}

func TestTranscriptTriggerGapIgnoresIntermediateAndPreStartFinals(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	promptAt := startedAt.Add(time.Second)
	session, events := triggerGapTurn(promptAt, "final")
	tracker := newTranscriptTriggerTracker(startedAt)
	detectedAt := promptAt.Add(3 * time.Second)

	tracker.observeWatchdog(session, events[:2], "live_card", detectedAt)
	if gaps := tracker.flushDue(detectedAt.Add(transcriptTriggerGrace)); len(gaps) != 0 {
		t.Fatalf("intermediate rows produced gap: %#v", gaps)
	}
	oldSession, oldEvents := triggerGapTurn(startedAt.Add(-time.Minute), "old final")
	tracker.observeWatchdog(oldSession, oldEvents, "card_reconcile", detectedAt)
	if gaps := tracker.flushDue(detectedAt.Add(2 * transcriptTriggerGrace)); len(gaps) != 0 {
		t.Fatalf("pre-start final produced gap: %#v", gaps)
	}
}

func TestTranscriptTriggerStateIsBounded(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	tracker := newTranscriptTriggerTracker(startedAt)
	for index := 0; index < maxTranscriptTriggerStates+20; index++ {
		session, events := triggerGapTurn(startedAt.Add(time.Duration(index+1)*time.Second), "final")
		session.ID = domain.SessionID(fmt.Sprintf("session-%03d", index))
		tracker.confirm(session, events, startedAt.Add(time.Hour))
	}
	if len(tracker.sessions) != maxTranscriptTriggerStates {
		t.Fatalf("trigger states=%d want=%d", len(tracker.sessions), maxTranscriptTriggerStates)
	}
}

func TestTranscriptTriggerPendingStatesAreEvictedAfterGrace(t *testing.T) {
	startedAt := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	detectedAt := startedAt.Add(time.Hour)
	tracker := newTranscriptTriggerTracker(startedAt)
	for index := 0; index < maxTranscriptTriggerStates+20; index++ {
		session, events := triggerGapTurn(
			startedAt.Add(time.Duration(index+1)*time.Second), "final",
		)
		session.ID = domain.SessionID(fmt.Sprintf("pending-%03d", index))
		tracker.observeWatchdog(session, events, "background", detectedAt)
	}
	if len(tracker.sessions) <= maxTranscriptTriggerStates {
		t.Fatalf("pending states=%d, expected temporary overflow", len(tracker.sessions))
	}
	tracker.flushDue(detectedAt.Add(transcriptTriggerGrace))
	if len(tracker.sessions) != maxTranscriptTriggerStates {
		t.Fatalf("trigger states after grace=%d want=%d",
			len(tracker.sessions), maxTranscriptTriggerStates)
	}
}
