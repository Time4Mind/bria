package telegramapp

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestSettlementWaitsForLatestQueuedPrompt(t *testing.T) {
	lastInputAt := time.Unix(100, 0).UTC()
	handler := &Handler{}
	session := domain.Session{
		ID: "session", NodeID: "node", RuntimePhase: domain.RuntimeRunning,
		LastEventAt: lastInputAt,
	}
	events := []transcript.Event{
		{Kind: transcript.EventUserText, Text: "earlier prompt",
			Timestamp: time.Unix(90, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "earlier answer",
			Timestamp: time.Unix(110, 0).UTC().Format(time.RFC3339Nano)},
	}
	if handler.settleFromTranscript(
		context.Background(), application.Principal{UserID: 7}, session, events,
	) {
		t.Fatal("earlier answer settled a later queued prompt")
	}
}

func TestSettledQueuedPromptRejectsPreviousTurnFinal(t *testing.T) {
	promptAt := time.Unix(200, 0).UTC()
	session := domain.Session{
		ID: "session", NodeID: "node", RuntimePhase: domain.RuntimeIdle,
		LastEventAt: promptAt.Add(5 * time.Second),
		LastOperation: &domain.SessionOperationResult{
			OperationID: "current-prompt", Action: domain.ActionSendInput,
			Status: domain.OperationQueued, At: promptAt,
		},
	}
	if transcriptFinalBelongsToCurrentTurn(
		session, promptAt.Add(-time.Second), promptAt.Add(10*time.Second),
	) {
		t.Fatal("previous turn final matched the current queued prompt")
	}
}

func TestCurrentTurnAssistantResponseRefreshMode(t *testing.T) {
	promptAt := time.Unix(300, 0).UTC()
	session := domain.Session{LastOperation: &domain.SessionOperationResult{
		Action: domain.ActionSendInput, Status: domain.OperationQueued, At: promptAt,
	}}
	event := func(kind transcript.EventKind, offset time.Duration) transcript.Event {
		return transcript.Event{
			Kind: kind, Timestamp: promptAt.Add(offset).Format(time.RFC3339Nano),
		}
	}
	tests := []struct {
		name      string
		events    []transcript.Event
		wantDelay time.Duration
	}{
		{
			name: "previous answer while current prompt is not flushed",
			events: []transcript.Event{
				event(transcript.EventUserText, -time.Minute),
				event(transcript.EventAssistantFinal, -time.Minute+time.Second),
			},
			wantDelay: paneWorkingRefreshDelay,
		},
		{
			name: "current prompt still working",
			events: []transcript.Event{
				event(transcript.EventUserText, time.Second),
				event(transcript.EventThinking, 2*time.Second),
				event(transcript.EventToolCall, 3*time.Second),
			},
			wantDelay: paneWorkingRefreshDelay,
		},
		{
			name: "assistant response tail",
			events: []transcript.Event{
				event(transcript.EventUserText, time.Second),
				event(transcript.EventToolResult, 2*time.Second),
				event(transcript.EventAssistantText, 3*time.Second),
			},
			wantDelay: paneResponseRefreshDelay,
		},
		{
			name: "tool resumed after commentary",
			events: []transcript.Event{
				event(transcript.EventUserText, time.Second),
				event(transcript.EventAssistantText, 2*time.Second),
				event(transcript.EventToolCall, 3*time.Second),
			},
			wantDelay: paneWorkingRefreshDelay,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := nextPaneRefreshDelay(session, test.events); got != test.wantDelay {
				t.Fatalf("refresh delay=%v want=%v", got, test.wantDelay)
			}
		})
	}
}

func TestPaneRefreshCadenceContract(t *testing.T) {
	if paneInitialDelay != 1500*time.Millisecond {
		t.Fatalf("initial delay=%v want=1.5s", paneInitialDelay)
	}
	if paneResponseRefreshDelay != 1500*time.Millisecond {
		t.Fatalf("response delay=%v want=1.5s", paneResponseRefreshDelay)
	}
	if paneWorkingRefreshDelay != 2500*time.Millisecond {
		t.Fatalf("working delay=%v want=2.5s", paneWorkingRefreshDelay)
	}
}

func TestCardEventsHideOnlyTrailingAssistantMemoryMetadata(t *testing.T) {
	metadata := "<oai-mem-citation>\n<citation_entries>\ninternal\n" +
		"</citation_entries>\n</oai-mem-citation>"
	events := cardEvents([]transcript.Event{
		{Kind: transcript.EventUserText, Text: "keep user " + metadata},
		{Kind: transcript.EventAssistantText, Text: "keep incomplete <oai-mem-citation>"},
		{Kind: transcript.EventAssistantText, Text: "keep inline " + metadata},
		{Kind: transcript.EventAssistantFinal, Text: "Visible answer\n\n" + metadata},
	})
	if len(events) != 4 {
		t.Fatalf("card events = %#v", events)
	}
	if events[0].Text != "keep user "+metadata {
		t.Fatalf("user metadata-shaped text changed: %q", events[0].Text)
	}
	if events[1].Text != "keep incomplete <oai-mem-citation>" {
		t.Fatalf("incomplete assistant text changed: %q", events[1].Text)
	}
	if events[2].Text != "keep inline "+metadata {
		t.Fatalf("inline assistant text changed: %q", events[2].Text)
	}
	if events[3].Text != "Visible answer" {
		t.Fatalf("trailing assistant metadata remained: %q", events[3].Text)
	}
}

func TestPaneAnchorUsesSessionContentBoundary(t *testing.T) {
	text := "answer\n\ncontext: 42%\n\nbackground"
	screen := telegramui.Screen{Text: text, PaneAnchorOffset: len("answer")}
	if got := paneAnchorOffset(screen); got != len("answer") {
		t.Fatalf("pane anchor = %d", got)
	}
}

func TestCardTranscriptCacheRetainsHistoryWhenReaderWindowShrinks(t *testing.T) {
	contextBefore := 41
	contextAfter := 43
	previous := []transcript.Event{
		{Kind: transcript.EventUserText, Text: "first", Timestamp: "2026-08-17T10:00:00Z"},
		{Kind: transcript.EventAssistantFinal, Text: "first answer", Timestamp: "2026-08-17T10:00:01Z"},
		{Kind: transcript.EventUserText, Text: "second", Timestamp: "2026-08-17T10:00:02Z"},
		{Kind: transcript.EventAssistantFinal, Text: "second answer", Timestamp: "2026-08-17T10:00:03Z", ContextPercent: &contextBefore},
	}
	shrunk := []transcript.Event{
		{Kind: transcript.EventUserText, Text: "second", Timestamp: "2026-08-17T10:00:02Z"},
		{Kind: transcript.EventAssistantFinal, Text: "second answer", Timestamp: "2026-08-17T10:00:03Z"},
		{Kind: transcript.EventUserText, Text: "third", Timestamp: "2026-08-17T10:00:04Z"},
		{Kind: transcript.EventAssistantFinal, Text: "third answer", Timestamp: "2026-08-17T10:00:05Z", ContextPercent: &contextAfter},
	}
	merged := mergeCardTranscriptEvents(previous, shrunk)
	if len(merged) != 6 || merged[0].Text != "first" ||
		merged[len(merged)-1].Text != "third answer" {
		t.Fatalf("merged transcript = %#v", merged)
	}
	if percent, ok := latestContextPercent(merged); !ok || percent != contextAfter {
		t.Fatalf("merged context = %d / %v", percent, ok)
	}
	merged[0].Text = "mutated"
	if previous[0].Text != "first" || shrunk[0].Text != "second" {
		t.Fatal("merged transcript aliases an input slice")
	}
}
