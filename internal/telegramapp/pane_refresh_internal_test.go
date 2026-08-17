package telegramapp

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
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
