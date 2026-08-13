package application_test

import (
	"reflect"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestHiddenTechnicalCardEventsLeaveOnlyConversationText(t *testing.T) {
	preferences := domain.DefaultUserPreferences()
	for _, eventType := range []domain.CardEventType{
		domain.CardEventToolCall, domain.CardEventToolResult, domain.CardEventThinking,
	} {
		if err := preferences.SetCardEventVisibility(eventType, false); err != nil {
			t.Fatal(err)
		}
	}
	events := []application.CardEvent{
		{Kind: application.CardEventUserText, Text: "request"},
		{Kind: application.CardEventThinking, Text: "private reasoning"},
		{Kind: application.CardEventToolCall, Text: "Bash"},
		{Kind: application.CardEventToolResult, Text: "command output"},
		{Kind: application.CardEventAssistantText, Text: "answer"},
	}
	want := []application.CardEvent{
		{Kind: application.CardEventUserText, Text: "request"},
		{Kind: application.CardEventAssistantText, Text: "answer"},
	}
	if got := application.VisibleCardEvents(preferences, events); !reflect.DeepEqual(got, want) {
		t.Fatalf("visible events=%#v", got)
	}
}
