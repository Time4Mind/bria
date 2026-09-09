package safelog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/safelog"
)

func TestSelectionFieldsPersistWithClosedVocabulary(t *testing.T) {
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	ref := "c_" + strings.Repeat("a", 64)
	fields := map[string]string{
		"stage":    "selection.fallback",
		"node_ref": ref, "previous_session_ref": ref, "target_session_ref": ref,
		"parent_operation_ref": ref, "provider_session_ref": ref, "approval_ref": ref,
		"candidate_count":  "18446744073709551615",
		"selection_reason": "durable_selectable", "selection_outcome": "selected",
	}
	if err := logger.Write(safelog.Event{Class: safelog.Detailed, Type: "telegram.flow_stage", Fields: fields}); err != nil {
		t.Fatal(err)
	}
	records, err := logger.Read(safelog.Detailed)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	for key, want := range fields {
		if got := records[0].Fields[key]; got != want {
			t.Errorf("%s=%q want %q", key, got, want)
		}
	}
	bad := map[string]string{}
	for key := range fields {
		bad[key] = "private_payload_marker"
	}
	if err := logger.Write(safelog.Event{Class: safelog.Detailed, Type: "telegram.flow_stage", Fields: bad}); err != nil {
		t.Fatal(err)
	}
	records, err = logger.Read(safelog.Detailed)
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	for _, key := range []string{"stage", "selection_reason", "selection_outcome"} {
		if records[1].Fields[key] != "unknown" {
			t.Errorf("%s must fail closed: %v", key, records[1].Fields)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private_payload_marker") {
		t.Fatal("payload leaked")
	}
}
