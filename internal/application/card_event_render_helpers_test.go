package application_test

import (
	"time"

	"github.com/Time4Mind/bria/internal/application"
)

func visibilityEvents() []application.CardEvent {
	return []application.CardEvent{
		{Kind: application.CardEventUserText, Text: "request"},
		{Kind: application.CardEventThinking, Body: "private-reasoning", StartedAt: cardTime(9, 59)},
		{
			Kind: application.CardEventToolCall, Text: "Bash", Body: "secret-command",
			ToolUseID: "tool-1", StartedAt: cardTime(10, 0),
		},
		{
			Kind: application.CardEventToolResult, Text: "Bash · done", Body: "command-output",
			ToolUseID: "tool-1", StartedAt: cardTime(10, 1),
		},
		{Kind: application.CardEventAssistantText, Text: "answer"},
	}
}

func fixedCardOptions(limit int) application.CardRenderOptions {
	return application.CardRenderOptions{
		Now: cardTime(10, 2), Location: time.UTC, MaxPageRunes: limit,
	}
}

func cardTime(hour, minute int) time.Time {
	return time.Date(2026, time.August, 10, hour, minute, 0, 0, time.UTC)
}
