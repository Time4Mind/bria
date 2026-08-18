package telegramapp

import (
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/transcript"
)

func cardEvents(events []transcript.Event) []application.CardEvent {
	result := make([]application.CardEvent, 0, len(events))
	for _, event := range events {
		kind, pageBreak := cardEventKind(event.Kind)
		if kind == "" {
			continue
		}
		startedAt, _ := time.Parse(time.RFC3339Nano, event.Timestamp)
		text := event.Text
		if event.Kind == transcript.EventAssistantText ||
			event.Kind == transcript.EventAssistantFinal {
			text = stripTrailingAssistantMetadata(text)
		}
		if event.Head != "" {
			text = event.Head
		}
		result = append(result, application.CardEvent{
			Kind: kind, Text: text, Body: event.Body,
			ToolUseID: event.ToolUseID, ToolName: event.ToolName,
			StartedAt: startedAt, IsError: event.Error, PageBreak: pageBreak,
		})
	}
	return result
}

const (
	assistantMetadataOpen  = "<oai-mem-citation>"
	assistantMetadataClose = "</oai-mem-citation>"
)

// stripTrailingAssistantMetadata removes transport-only metadata appended by
// Codex after the user-facing answer. It intentionally requires a complete
// trailing block and runs only for assistant events, so user text, code samples,
// ordinary HTML, and incomplete tag-shaped content remain visible verbatim.
func stripTrailingAssistantMetadata(text string) string {
	trimmed := strings.TrimSpace(text)
	for strings.HasSuffix(trimmed, assistantMetadataClose) {
		start := strings.LastIndex(trimmed, assistantMetadataOpen)
		if start < 0 || (start > 0 && trimmed[start-1] != '\n') {
			break
		}
		trimmed = strings.TrimSpace(trimmed[:start])
	}
	return trimmed
}

func cardEventKind(kind transcript.EventKind) (application.CardEventKind, bool) {
	switch kind {
	case transcript.EventUserText:
		return application.CardEventUserText, false
	case transcript.EventAssistantText:
		return application.CardEventAssistantText, false
	case transcript.EventAssistantFinal:
		return application.CardEventAssistantText, true
	case transcript.EventThinking:
		return application.CardEventThinking, false
	case transcript.EventToolCall:
		return application.CardEventToolCall, false
	case transcript.EventToolResult:
		return application.CardEventToolResult, false
	default:
		return "", false
	}
}

func finalTranscriptAt(events []transcript.Event) (time.Time, bool) {
	final, ok := finalTranscriptEvent(events)
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, final.Timestamp)
	return at, err == nil
}

func finalTranscriptEvent(events []transcript.Event) (transcript.Event, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind != transcript.EventAssistantFinal {
			if events[index].Kind == transcript.EventUserText ||
				events[index].Kind == transcript.EventAssistantText ||
				events[index].Kind == transcript.EventThinking ||
				events[index].Kind == transcript.EventToolCall {
				return transcript.Event{}, false
			}
			continue
		}
		return events[index], true
	}
	return transcript.Event{}, false
}
