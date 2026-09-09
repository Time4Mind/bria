package safelog_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/safelog"
)

func TestTimingFieldsAreAllowlistedAsBoundedNonNegativeDecimals(t *testing.T) {
	dir := t.TempDir()
	// The timestamp deliberately contains "1.5", an invalid timing value.
	now := time.Date(2026, 9, 9, 6, 15, 31, 544917000, time.UTC)
	log, err := safelog.Open(safelog.Options{Directory: dir, Now: func() time.Time { return now }})
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
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 5 {
		t.Fatalf("timing event count = %d, want 5", len(lines))
	}
	for i, line := range lines {
		var event safelog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event %d: invalid JSON: %v", i, err)
		}
		if event.Class != safelog.Service || event.Type != "timing.terminal" || !event.Time.Equal(now) {
			t.Fatalf("event %d: unexpected envelope: %+v", i, event)
		}
		want := valid
		if i > 0 {
			want = map[string]string{"queue_wait_ms": "[REDACTED]"}
		}
		if len(event.Fields) != len(want) {
			t.Errorf("event %d: fields = %v, want %v", i, event.Fields, want)
		}
		for key, value := range want {
			if actual, ok := event.Fields[key]; !ok || actual != value {
				t.Errorf("event %d: field %s = %q, want %q", i, key, actual, value)
			}
		}
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
