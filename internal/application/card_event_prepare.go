package application

import (
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

// prepareCardEvents applies visibility before rendering and folds a visible
// tool result into its visible invocation. A hidden result may close the
// invocation timer, but its text and body never enter the prepared event.
func prepareCardEvents(
	preferences domain.UserPreferences,
	events []CardEvent,
) []CardEvent {
	prepared := make([]CardEvent, 0, len(events))
	toolCalls := make(map[string]int)
	hiddenToolCalls := make(map[string]CardEvent)
	for _, event := range events {
		if event.Kind == CardEventToolResult {
			resultVisible := cardEventVisible(preferences, event.Kind)
			callIndex, paired := toolCalls[event.ToolUseID]
			if event.ToolUseID != "" && paired {
				foldToolResult(&prepared[callIndex], event, resultVisible)
				continue
			}
			if resultVisible {
				if call, ok := hiddenToolCalls[event.ToolUseID]; ok {
					event.Text = call.Text
					event.ToolName = call.ToolName
				}
				prepared = append(prepared, event)
			}
			continue
		}
		if !cardEventVisible(preferences, event.Kind) {
			if event.Kind == CardEventToolCall && event.ToolUseID != "" {
				hiddenToolCalls[event.ToolUseID] = event
			}
			continue
		}
		prepared = append(prepared, event)
		if event.Kind == CardEventToolCall && event.ToolUseID != "" {
			toolCalls[event.ToolUseID] = len(prepared) - 1
		}
	}
	return prepared
}

func foldToolResult(call *CardEvent, result CardEvent, includeContent bool) {
	completedAt := result.CompletedAt
	if completedAt == nil {
		value := result.StartedAt
		completedAt = &value
	}
	call.CompletedAt = cloneTime(completedAt)
	call.IsError = result.IsError
	call.PageBreak = call.PageBreak || result.PageBreak
	if !includeContent {
		return
	}
	call.HasResult = true
	call.ResultBody = result.Body
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
