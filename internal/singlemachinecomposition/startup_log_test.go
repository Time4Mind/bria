package singlemachinecomposition

import (
	"errors"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/safelog"
)

func TestStartupLogNeverPersistsRawProviderError(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	intent := app.ConfirmedSessionIntent{IntentID: "test-start", ComputerID: "local", Provider: domain.ProviderClaude}
	recordSessionStartup(logger, intent, 12*time.Millisecond, errors.New("private credential and raw stderr"))
	recordSessionStartup(logger, intent, 15*time.Millisecond, nil)
	events, err := logger.Read(safelog.Service)
	if err != nil || len(events) != 2 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if events[0].Error != "" || events[0].ErrorCategory != "startup_failed" || events[0].Fields["duration_ms"] != "12" || events[1].Result != "ready" {
		t.Fatalf("unexpected sanitized startup events: %v", events)
	}
}
