package singlemachinecomposition

import (
	"context"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocesssession"
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

func TestPreprocessingSessionObserverPersistsSafeLifecycleOnly(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer := preprocessingSessionObserver{logger: logger}
	if err := observer.ObservePreprocessingSession(context.Background(), promptpreprocesssession.LifecycleObservation{
		State: "ready", Provider: domain.ProviderCodex, Model: "gpt-5.6-luna",
	}); err != nil {
		t.Fatal(err)
	}
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 1 {
		t.Fatalf("events = (%#v, %v)", events, err)
	}
	if events[0].Type != "prompt.preprocessing_session" || events[0].Fields["state"] != "ready" || events[0].Fields["model"] != "gpt-5.6-luna" || events[0].Fields["duration_ms"] != "0" {
		t.Fatalf("event = %#v", events[0])
	}
}

func TestPreprocessingObserverDistinguishesConfirmedRunFromCache(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer := preprocessingObserver{logger: logger}
	request := promptpreprocess.Request{ComputerID: "local", SessionID: "session-a", MessageID: "message-a", Text: "private prompt"}
	for _, observation := range []promptpreprocess.Observation{
		promptpreprocess.SuccessObservation(request, promptpreprocess.Result{Provider: domain.ProviderCodex, Model: "gpt-5.6-luna", ModelEvidence: "codex_cli_header"}),
		promptpreprocess.CachedObservation(request, false),
	} {
		if err := observer.ObservePreprocessing(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 2 {
		t.Fatalf("events: %v %v", events, err)
	}
	if events[0].Type != "prompt.preprocessing_completed" || events[0].Fields["model_evidence"] != "codex_cli_header" || events[0].Fields["model"] != "gpt-5.6-luna" {
		t.Fatalf("success: %#v", events[0])
	}
	if events[1].Type != "prompt.preprocessing_cached" || events[1].Fields["model"] != "" || events[1].Fields["attempt"] != "0" {
		t.Fatalf("cache: %#v", events[1])
	}
}
