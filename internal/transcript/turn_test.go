package transcript

import (
	"testing"
	"time"
)

func TestLatestCompletedTurnCarriesItsPromptBoundary(t *testing.T) {
	userAt := time.Unix(100, 0).UTC()
	finalAt := time.Unix(110, 0).UTC()
	turn, ok := LatestCompletedTurn([]Event{
		{Kind: EventAssistantFinal, Timestamp: time.Unix(90, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: EventUserText, Text: "latest prompt", Timestamp: userAt.Format(time.RFC3339Nano)},
		{Kind: EventAssistantText, Text: "working", Timestamp: time.Unix(105, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: EventAssistantFinal, Text: "answer", Timestamp: finalAt.Format(time.RFC3339Nano)},
	})
	if !ok || !turn.HasUser || turn.UserAt != userAt || turn.FinalAt != finalAt ||
		turn.Final.Text != "answer" {
		t.Fatalf("turn=%#v ok=%v", turn, ok)
	}
}

func TestLatestCompletedTurnRejectsNewerUnfinishedTurn(t *testing.T) {
	_, ok := LatestCompletedTurn([]Event{
		{Kind: EventUserText, Timestamp: time.Unix(100, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: EventAssistantFinal, Timestamp: time.Unix(110, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: EventUserText, Timestamp: time.Unix(120, 0).UTC().Format(time.RFC3339Nano)},
	})
	if ok {
		t.Fatal("older final completed a newer prompt")
	}
}
