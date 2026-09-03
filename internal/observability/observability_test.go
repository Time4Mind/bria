package observability_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/observability"
	"bria/internal/safelog"
)

const testSessionID = "9f4b7d2e-4d26-4a57-a8f0-80d39bf1e6c4"

func TestTerminalSpanWritesSuccessWithMonotonicDurationAndSafeMetrics(t *testing.T) {
	log, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.New(log)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := observability.NewScope(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	span, err := recorder.Start(scope, "provider.submit", "telegram-update-123456")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := span.Success(observability.Measurements{
		QueueWait:      observability.Duration(3 * time.Millisecond),
		ProviderAccept: observability.Duration(5 * time.Millisecond),
		FirstEvent:     observability.Duration(7 * time.Millisecond),
		Total:          observability.Duration(11 * time.Millisecond),
		HTTPToHeaders:  observability.Duration(13 * time.Millisecond),
		HTTPTotal:      observability.Duration(17 * time.Millisecond),
		RetryDelay:     observability.Duration(19 * time.Millisecond),
		Bytes:          observability.Count(23),
		OldestAge:      observability.Duration(29 * time.Millisecond),
		ActiveTurns:    observability.Count(31),
		UnknownCount:   observability.Count(37),
		FailedCount:    observability.Count(41),
	}); err != nil {
		t.Fatal(err)
	}

	records, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Type != "timing.terminal" || record.Result != "success" || record.ErrorCategory != "" {
		t.Fatalf("terminal record = %+v", record)
	}
	if record.Time.Location() != time.UTC {
		t.Fatalf("logger time location = %s, want UTC", record.Time.Location())
	}
	if got := record.Fields["duration_ms"]; got == "" || got == "0" {
		t.Fatalf("duration_ms = %q, want positive monotonic elapsed time", got)
	}
	for key, want := range map[string]string{
		"queue_wait_ms": "3", "provider_accept_ms": "5", "first_event_ms": "7", "total_ms": "11",
		"http_to_headers_ms": "13", "http_total_ms": "17", "retry_delay_ms": "19", "bytes": "23",
		"oldest_age_ms": "29", "active_turns": "31", "unknown_count": "37", "failed_count": "41",
	} {
		if got := record.Fields[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestTerminalSpanWritesSafeErrorAndDoesNotLeakRawCorrelationInputs(t *testing.T) {
	directory := t.TempDir()
	log, err := safelog.Open(safelog.Options{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.New(log)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := observability.NewScope(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	occurrenceID := "telegram-update-123456"
	span, err := recorder.Start(scope, "provider.submit", occurrenceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := span.Failure("provider_timeout", observability.Measurements{}); err != nil {
		t.Fatal(err)
	}

	records, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	record := records[0]
	if record.Result != "error" || record.ErrorCategory != "provider_timeout" {
		t.Fatalf("error record = %+v", record)
	}
	wantHash := sha256.Sum256([]byte("bria-observability-v1\x00" + testSessionID + "\x00" + occurrenceID + "\x00provider.submit"))
	wantCorrelation := "c_" + hex.EncodeToString(wantHash[:])
	if record.EntityID != wantCorrelation {
		t.Fatalf("correlation = %q, want %q", record.EntityID, wantCorrelation)
	}
	persisted, err := os.ReadFile(filepath.Join(directory, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), testSessionID) || strings.Contains(string(persisted), occurrenceID) {
		t.Fatalf("raw correlation input leaked in %q", persisted)
	}
}

func TestCorrelationIsDeterministicAndSeparatedByExactOperation(t *testing.T) {
	scope, err := observability.NewScope(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := scope.Correlation("provider.submit", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := scope.Correlation("provider.submit", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	other, err := scope.Correlation("provider.submit", "turn-2")
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == other {
		t.Fatalf("correlations first=%q again=%q other=%q", first, again, other)
	}
}

func TestRejectsUnsafeScopeLabelsAndMeasurements(t *testing.T) {
	log, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.New(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, sessionID := range []string{"", "123456789", "9f4b7d2e-4d26-5a57-a8f0-80d39bf1e6c4", "9f4b7d2e-4d26-4a57-78f0-80d39bf1e6c4"} {
		if _, err := observability.NewScope(sessionID); !errors.Is(err, observability.ErrInvalidScope) {
			t.Errorf("NewScope(%q) error = %v, want ErrInvalidScope", sessionID, err)
		}
	}
	scope, err := observability.NewScope(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Start(scope, "contains private message", "turn-1"); !errors.Is(err, observability.ErrInvalidOperation) {
		t.Fatalf("unsafe operation error = %v, want ErrInvalidOperation", err)
	}
	span, err := recorder.Start(scope, "provider.submit", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := span.Success(observability.Measurements{QueueWait: observability.Duration(-time.Millisecond)}); !errors.Is(err, observability.ErrInvalidMeasurements) {
		t.Fatalf("negative measurement error = %v, want ErrInvalidMeasurements", err)
	}
	if err := span.Success(observability.Measurements{Bytes: observability.Count(^uint64(0))}); !errors.Is(err, observability.ErrInvalidMeasurements) {
		t.Fatalf("overflow measurement error = %v, want ErrInvalidMeasurements", err)
	}
	if err := span.Success(observability.Measurements{}); err != nil {
		t.Fatal(err)
	}
	if err := span.Success(observability.Measurements{}); !errors.Is(err, observability.ErrAlreadyTerminal) {
		t.Fatalf("second terminal error = %v, want ErrAlreadyTerminal", err)
	}
}

func TestMeasurementsOmitAbsentFieldsButPreserveMeasuredZero(t *testing.T) {
	log, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := observability.New(log)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := observability.NewScope(testSessionID)
	if err != nil {
		t.Fatal(err)
	}
	absent, err := recorder.Start(scope, "provider.submit", "turn-absent")
	if err != nil {
		t.Fatal(err)
	}
	if err := absent.Success(observability.Measurements{}); err != nil {
		t.Fatal(err)
	}
	zero, err := recorder.Start(scope, "provider.submit", "turn-zero")
	if err != nil {
		t.Fatal(err)
	}
	if err := zero.Success(observability.Measurements{QueueWait: observability.Duration(0), Bytes: observability.Count(0)}); err != nil {
		t.Fatal(err)
	}
	records, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := records[0].Fields["queue_wait_ms"]; exists {
		t.Fatalf("absent measurement was emitted: %+v", records[0].Fields)
	}
	if records[1].Fields["queue_wait_ms"] != "0" || records[1].Fields["bytes"] != "0" {
		t.Fatalf("measured zero was not emitted: %+v", records[1].Fields)
	}
}
