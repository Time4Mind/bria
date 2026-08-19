package transcript

import (
	"testing"
	"time"
)

func TestLatestRuntimeObservationTracksOpenAndCompletedCodexTurn(t *testing.T) {
	completeAt := time.Unix(20, 0).UTC()
	activityAt := time.Unix(30, 0).UTC()
	completed := []Event{
		{Kind: EventAssistantFinal, Timestamp: time.Unix(19, 0).UTC().Format(time.RFC3339Nano)},
		{Kind: EventTurnComplete, Timestamp: completeAt.Format(time.RFC3339Nano)},
	}
	observation, ok := LatestRuntimeObservation(completed, BackendCodex)
	if !ok || observation.Running || observation.At != completeAt {
		t.Fatalf("completed observation=%+v ok=%v", observation, ok)
	}
	open := append(completed, Event{
		Kind: EventToolCall, Timestamp: activityAt.Format(time.RFC3339Nano),
	})
	observation, ok = LatestRuntimeObservation(open, BackendCodex)
	if !ok || !observation.Running || observation.At != activityAt {
		t.Fatalf("open observation=%+v ok=%v", observation, ok)
	}
}

func TestLatestRuntimeObservationRejectsUnsupportedOrMalformedEvidence(t *testing.T) {
	if _, ok := LatestRuntimeObservation([]Event{{Kind: EventAssistantFinal}}, BackendClaude); ok {
		t.Fatal("Claude runtime observation unexpectedly accepted")
	}
	if _, ok := LatestRuntimeObservation(
		[]Event{{Kind: EventTurnComplete, Timestamp: "invalid"}}, BackendCodex,
	); ok {
		t.Fatal("malformed Codex runtime observation accepted")
	}
}
