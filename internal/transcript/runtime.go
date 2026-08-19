package transcript

import "time"

// RuntimeObservation describes the latest provider-owned evidence about whether
// a turn is still open. It carries no transcript body and is safe to publish as
// bounded runtime metadata.
type RuntimeObservation struct {
	Running bool
	At      time.Time
}

// LatestRuntimeObservation currently uses Codex's explicit task_complete
// boundary. Claude does not expose an equivalent durable start/stop pair, so
// its existing assistant-final reconciliation remains authoritative.
func LatestRuntimeObservation(events []Event, backend Backend) (RuntimeObservation, bool) {
	if backend != BackendCodex || len(events) == 0 {
		return RuntimeObservation{}, false
	}
	complete := -1
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == EventTurnComplete {
			complete = index
			break
		}
	}
	if complete >= 0 && !hasRuntimeActivity(events[complete+1:]) {
		at, err := time.Parse(time.RFC3339Nano, events[complete].Timestamp)
		if err != nil {
			return RuntimeObservation{}, false
		}
		return RuntimeObservation{At: at.UTC()}, true
	}
	for index := len(events) - 1; index > complete; index-- {
		if !isRuntimeActivity(events[index].Kind) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, events[index].Timestamp)
		if err != nil {
			return RuntimeObservation{}, false
		}
		return RuntimeObservation{Running: true, At: at.UTC()}, true
	}
	return RuntimeObservation{}, false
}

func hasRuntimeActivity(events []Event) bool {
	for _, event := range events {
		if isRuntimeActivity(event.Kind) {
			return true
		}
	}
	return false
}

func isRuntimeActivity(kind EventKind) bool {
	switch kind {
	case EventUserText, EventAssistantText, EventAssistantFinal, EventThinking,
		EventToolCall, EventToolResult:
		return true
	default:
		return false
	}
}
