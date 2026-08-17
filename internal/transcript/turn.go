package transcript

import "time"

// CompletedTurn identifies the latest provider answer and, when it is still
// present in the bounded transcript window, the prompt that answer belongs to.
type CompletedTurn struct {
	Final   Event
	FinalAt time.Time
	UserAt  time.Time
	HasUser bool
}

func LatestCompletedTurn(events []Event) (CompletedTurn, bool) {
	finalIndex := -1
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == EventAssistantFinal {
			finalIndex = index
			break
		}
		switch events[index].Kind {
		case EventUserText, EventAssistantText, EventThinking, EventToolCall:
			return CompletedTurn{}, false
		}
	}
	if finalIndex < 0 {
		return CompletedTurn{}, false
	}
	finalAt, err := time.Parse(time.RFC3339Nano, events[finalIndex].Timestamp)
	if err != nil {
		return CompletedTurn{}, false
	}
	turn := CompletedTurn{Final: events[finalIndex], FinalAt: finalAt.UTC()}
	for index := finalIndex - 1; index >= 0; index-- {
		if events[index].Kind != EventUserText {
			continue
		}
		userAt, err := time.Parse(time.RFC3339Nano, events[index].Timestamp)
		if err != nil {
			return CompletedTurn{}, false
		}
		turn.UserAt = userAt.UTC()
		turn.HasUser = true
		break
	}
	return turn, true
}
