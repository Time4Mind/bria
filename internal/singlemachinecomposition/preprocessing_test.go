package singlemachinecomposition

import (
	"context"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/safelog"
)

func TestPreprocessingObserverPersistsIdentityWithoutPrompt(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer := preprocessingObserver{logger: logger}
	if err := observer.ObservePreprocessing(context.Background(), promptpreprocess.Observation{
		ComputerID: "local", SessionID: domain.SessionID("session-a"), MessageID: "message-a",
		Provider: domain.ProviderCodex, Model: "cheap", Stage: "invoke", Category: "timeout",
		Attempts: 1, Error: "deadline exceeded",
	}); err != nil {
		t.Fatal(err)
	}
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = (%#v, %v)", events, err)
	}
	event := events[0]
	if event.Type != "prompt.preprocessing_failed" || event.EntityID != "session-a" || event.Fields["message_id"] != "message-a" || event.Fields["attempt"] != "1" || event.Fields["model"] != "cheap" || event.Fields["stage"] != "invoke" || event.ErrorCategory != "timeout" {
		t.Fatalf("event = %#v", event)
	}
	encoded := event.Error + event.EntityID
	for key, value := range event.Fields {
		encoded += key + value
	}
	if strings.Contains(encoded, "private prompt") {
		t.Fatal("prompt leaked into preprocessing observation")
	}
}
