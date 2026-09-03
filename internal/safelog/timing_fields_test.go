package safelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/safelog"
)

func TestTimingFieldsAreAllowlistedAsBoundedNonNegativeDecimals(t *testing.T) {
	dir := t.TempDir()
	log, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]string{
		"queue_wait_ms": "0", "provider_accept_ms": "1", "first_event_ms": "2", "total_ms": "3",
		"http_to_headers_ms": "4", "http_total_ms": "5", "retry_delay_ms": "6", "bytes": "7",
		"oldest_age_ms": "8", "active_turns": "9", "unknown_count": "10", "failed_count": "11",
	}
	if err := log.Write(safelog.Event{Class: safelog.Service, Type: "timing.terminal", Fields: valid}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"-1", "+1", "1.5", "9223372036854775808"} {
		if err := log.Write(safelog.Event{Class: safelog.Service, Type: "timing.terminal", Fields: map[string]string{"queue_wait_ms": invalid}}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range valid {
		if !strings.Contains(string(raw), `"`+key+`":"`+value+`"`) {
			t.Errorf("valid timing field %s=%s was not persisted: %s", key, value, raw)
		}
	}
	for _, invalid := range []string{"-1", "+1", "1.5", "9223372036854775808"} {
		if strings.Contains(string(raw), invalid) {
			t.Errorf("invalid timing value leaked: %q in %s", invalid, raw)
		}
	}
	if count := strings.Count(string(raw), `"queue_wait_ms":"[REDACTED]"`); count != 4 {
		t.Fatalf("redacted invalid count = %d, want 4: %s", count, raw)
	}
}

func TestTimingEventUsesExistingServiceRetention(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	log, err := safelog.Open(safelog.Options{Directory: directory, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Write(safelog.Event{
		Class:  safelog.Service,
		Type:   "timing.terminal",
		Fields: map[string]string{"queue_wait_ms": "1"},
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	if err := log.Cleanup(); err != nil {
		t.Fatal(err)
	}
	records, err := log.Read(safelog.Service)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("timing record survived existing service retention: %+v", records)
	}
}
