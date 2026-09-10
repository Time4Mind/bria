package singlemachinecomposition

import (
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/runtimediagnostic"
	"bria/internal/safelog"
)

func TestStartupLogNeverPersistsRawProviderError(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	intent := app.ConfirmedSessionIntent{IntentID: "test-start", ComputerID: "local", Provider: domain.ProviderClaude}
	recordSessionStartup(logger, intent, "session-failed", 3, 12*time.Millisecond, errors.New("private credential and raw stderr"))
	recordSessionStartup(logger, intent, "session-ready", 2, 15*time.Millisecond, nil)
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if events[0].Error != "" || events[0].EntityID != "session-failed" || events[0].ErrorCategory != "startup_failed" ||
		events[0].Fields["duration_ms"] != "12" || events[0].Fields["attempt"] != "3" ||
		events[1].EntityID != "session-ready" || events[1].Fields["attempt"] != "2" || events[1].Result != "ready" {
		t.Fatalf("unexpected sanitized startup events: %v", events)
	}
}

func TestStartupLogIncludesAllowlistedFailureStage(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := &runtimediagnostic.Drain{}
	_, _ = diagnostic.Write([]byte("bria-native-startup:binding_persistence:adapter_failed\n"))
	diagnostic.Finish()
	failure := diagnostic.Wrap(errors.New("private binding persistence error"))
	recordSessionStartup(logger, app.ConfirmedSessionIntent{ComputerID: "local", Provider: domain.ProviderCodex}, "session-stage", 1, time.Millisecond, failure)
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if events[0].Fields["stage"] != "binding_persistence" || events[0].ErrorCategory != "adapter_failed" {
		t.Fatalf("startup classification = %#v", events[0])
	}
}

func TestStartupAttemptLogCorrelatesFailureWithoutRawError(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := &runtimediagnostic.Drain{}
	_, _ = diagnostic.Write([]byte("bria-native-startup:open_terminal:adapter_failed\n"))
	diagnostic.Finish()
	recordSessionStartupAttempt(logger, app.InitialStartAttemptFailure{
		SessionID: "session-attempt", ComputerID: "local", Provider: domain.ProviderCodex,
		Attempt: 2, Err: diagnostic.Wrap(errors.New("private raw provider text")),
	})
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if events[0].Type != "session.startup_attempt" || events[0].EntityID != "session-attempt" || events[0].Error != "" ||
		events[0].ErrorCategory != "adapter_failed" || events[0].Fields["attempt"] != "2" || events[0].Fields["stage"] != "open_terminal" {
		t.Fatalf("unsafe or uncorrelated startup attempt = %#v", events[0])
	}
}

func TestRecoveryFailureLogCorrelatesSessionWithoutRawError(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := &runtimediagnostic.Drain{}
	_, _ = diagnostic.Write([]byte("bria-native-startup:protocol_ready_emission:adapter_failed\n"))
	diagnostic.Finish()
	recordSessionRecoveryFailure(logger, "session-recovery", diagnostic.Wrap(errors.New("private recovery output")))
	events, err := logger.Read(safelog.Critical)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	event := events[0]
	if event.Type != "session.recovery_failed" || event.EntityID != "session-recovery" || event.Error != "" ||
		event.Result != "failed" || event.ErrorCategory != "adapter_failed" || event.Fields["stage"] != "protocol_ready_emission" {
		t.Fatalf("unsafe or uncorrelated recovery failure = %#v", event)
	}
}
