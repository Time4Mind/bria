package singlemachinecomposition

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/preprocessinglog"
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocesssession"
	"bria/internal/safelog"
)

type preprocessingInputReferencer func(string) string

func (f preprocessingInputReferencer) InputRef(operation string) string { return f(operation) }

const preprocessingRef = "c_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestPreprocessingObserverPersistsIdentityWithoutPrompt(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer := preprocessinglog.Observer{Logger: logger, Inputs: preprocessingInputReferencer(func(string) string { return preprocessingRef })}
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
	if event.Type != "prompt.preprocessing_failed" || event.EntityID != "" || event.Fields["input_ref"] != preprocessingRef || event.Fields["attempt"] != "1" || event.Fields["model"] != "cheap" || event.Fields["stage"] != "invoke" || event.ErrorCategory != "timeout" {
		t.Fatalf("event = %#v", event)
	}
	encoded := event.Error + event.EntityID
	for key, value := range event.Fields {
		encoded += key + value
	}
	if strings.Contains(encoded, "private prompt") || strings.Contains(encoded, "session-a") || strings.Contains(encoded, "message-a") {
		t.Fatal("prompt leaked into preprocessing observation")
	}
}

func TestPreprocessingSessionObserverPersistsSafeLifecycleOnly(t *testing.T) {
	logDir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: logDir})
	if err != nil {
		t.Fatal(err)
	}
	observer := preprocessinglog.SessionObserver{Logger: logger, Inputs: preprocessingInputReferencer(func(string) string { return preprocessingRef })}
	for _, observation := range []promptpreprocesssession.LifecycleObservation{
		{State: "start_failed", Provider: domain.ProviderCodex, Model: "gpt-5.6-luna", ErrorCategory: "thread_not_found"},
		{State: "responded", MessageID: "telegram-update:71", Provider: domain.ProviderCodex, Model: "gpt-5.6-luna"},
	} {
		if err := observer.ObservePreprocessingSession(context.Background(), observation); err != nil {
			t.Fatal(err)
		}
	}
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 2 {
		t.Fatalf("events = (%#v, %v)", events, err)
	}
	if events[0].Type != "prompt.preprocessing_session" || events[0].Fields["state"] != "start_failed" || events[0].ErrorCategory != "thread_not_found" ||
		events[1].Fields["state"] != "responded" || events[1].Fields["input_ref"] != preprocessingRef || events[1].ErrorCategory != "" || events[1].Fields["model"] != "gpt-5.6-luna" || events[1].Fields["duration_ms"] != "0" {
		t.Fatalf("events = %#v", events)
	}
	raw, err := os.ReadFile(filepath.Join(logDir, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"provider-thread-secret", "private-token", "https://"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("service log exposed private lifecycle detail %q", forbidden)
		}
	}
}

func TestPreprocessingObserverDistinguishesConfirmedRunFromCache(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer := preprocessinglog.Observer{Logger: logger}
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
