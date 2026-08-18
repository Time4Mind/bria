package application

import (
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type CardEventKind string

const (
	CardEventUserText      CardEventKind = "user_text"
	CardEventAssistantText CardEventKind = "assistant_text"
	CardEventToolCall      CardEventKind = "tool_call"
	CardEventToolResult    CardEventKind = "tool_result"
	CardEventThinking      CardEventKind = "thinking"
)

type CardEvent struct {
	ID          string
	Kind        CardEventKind
	Text        string
	Body        string
	ResultBody  string
	HasResult   bool
	ToolUseID   string
	ToolName    string
	StartedAt   time.Time
	CompletedAt *time.Time
	IsError     bool
	PageBreak   bool
}

// VisibleCardEvents is the single filtering boundary used before transcript
// events reach Telegram. Hidden technical events leave no heading, spoiler,
// or placeholder; narrative conversation text is preserved.
func VisibleCardEvents(
	preferences domain.UserPreferences,
	events []CardEvent,
) []CardEvent {
	visible := make([]CardEvent, 0, len(events))
	for _, event := range events {
		if cardEventVisible(preferences, event.Kind) {
			visible = append(visible, event)
		}
	}
	return visible
}

func cardEventVisible(preferences domain.UserPreferences, kind CardEventKind) bool {
	switch kind {
	case CardEventToolCall:
		return preferences.ShowsCardEvent(domain.CardEventToolCall)
	case CardEventToolResult:
		return preferences.ShowsCardEvent(domain.CardEventToolResult)
	case CardEventThinking:
		return preferences.ShowsCardEvent(domain.CardEventThinking)
	default:
		return true
	}
}
