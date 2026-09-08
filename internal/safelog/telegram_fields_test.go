package safelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/safelog"
)

func TestTelegramObservedTimeAndClosedFieldsPersist(t *testing.T) {
	now := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	observed := now.Add(-time.Minute)
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ref := "c_" + strings.Repeat("a", 64)
	fields := map[string]string{
		"callback_ref": ref, "card_ref": ref, "expected_card_ref": ref,
		"session_ref": ref, "presentation_ref": ref, "run_ref": ref,
		"button_refs": strings.TrimSuffix(strings.Repeat(ref+",", 32), ","),
		"sequence":    "18446744073709551615", "page": "0", "pages": "4",
		"button_refs_truncated": "8",
		"target":                "3",
		"follow_latest":         "false", "retired": "true", "reason": "presentation_replayed",
	}
	if err := logger.Write(safelog.Event{Class: safelog.Detailed, Type: "telegram.flow_stage", Time: observed, Fields: fields}); err != nil {
		t.Fatal(err)
	}
	records, err := logger.Read(safelog.Detailed)
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %v, %v", records, err)
	}
	if !records[0].Time.Equal(observed) {
		t.Errorf("observed time lost: %v", records[0].Time)
	}
	for key, want := range fields {
		if got := records[0].Fields[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestTelegramClosedFieldsRejectMalformedValuesInPhysicalJSON(t *testing.T) {
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	ref := "c_" + strings.Repeat("a", 64)
	for _, bad := range []string{"private_payload_marker", "-987654321876", ref + "extra", "c_" + strings.Repeat("Z", 64), "", "true", "+1", "1.5", "18446744073709551616"} {
		fields := map[string]string{}
		for _, key := range []string{"callback_ref", "card_ref", "expected_card_ref", "session_ref", "presentation_ref", "run_ref", "button_refs", "page", "pages", "sequence", "button_refs_truncated", "target"} {
			fields[key] = bad
		}
		fields["follow_latest"], fields["retired"] = "private_payload_marker", "1"
		fields["reason"] = "private_payload_marker"
		fields["unregistered_ref"] = ref
		if err := logger.Write(safelog.Event{Class: safelog.Detailed, Type: "telegram.flow_stage", ErrorCategory: "private_payload_marker", Error: "private_payload_marker", Fields: fields}); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Write(safelog.Event{Class: safelog.Detailed, Type: "telegram.flow_stage", Fields: map[string]string{"button_refs": strings.TrimSuffix(strings.Repeat(ref+",", 33), ",")}}); err != nil {
		t.Fatal(err)
	}
	records, err := logger.Read(safelog.Detailed)
	if err != nil || len(records) != 10 {
		t.Fatalf("records=%d, %v", len(records), err)
	}
	for _, record := range records[:9] {
		if record.ErrorCategory != "operation_failed" || record.Fields["reason"] != "operation_failed" {
			t.Error("reason failed open")
		}
		for key, value := range record.Fields {
			if key != "reason" && value != "[REDACTED]" {
				t.Errorf("invalid %s was preserved: %q", key, value)
			}
		}
	}
	if records[9].Fields["button_refs"] != "[REDACTED]" {
		t.Error("oversized reference list persisted")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_payload_marker", "987654321876", "unregistered_ref"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("unsafe field leaked: %s", forbidden)
		}
	}
}
