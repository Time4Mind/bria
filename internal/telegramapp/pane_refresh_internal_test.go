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
