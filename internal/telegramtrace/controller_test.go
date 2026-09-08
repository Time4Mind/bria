package telegramtrace_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/controllertelemetry"
	"bria/internal/safelog"
	"bria/internal/telegramtrace"
)

func TestControllerFrozenVocabularySurvivesPhysicalJSONL(t *testing.T) {
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	stages := []struct {
		value controllertelemetry.Stage
		wire  string
	}{
		{controllertelemetry.StageUnknown, "unknown"}, {controllertelemetry.ArchiveOutcome, "session.archive_outcome"},
		{controllertelemetry.FallbackChoice, "selection.fallback"}, {controllertelemetry.SelectionPersist, "selection.persist"},
		{controllertelemetry.ProjectedTarget, "selection.project"}, {255, "unknown"},
	}
	outcomes := []struct {
		value controllertelemetry.Outcome
		wire  string
	}{
		{controllertelemetry.OutcomeUnknown, "unknown"}, {controllertelemetry.Scheduled, "scheduled"},
		{controllertelemetry.Archived, "archived"}, {controllertelemetry.Deleted, "deleted"},
		{controllertelemetry.Selected, "selected"}, {controllertelemetry.Cleared, "cleared"},
		{controllertelemetry.Preserved, "preserved"}, {controllertelemetry.Persisted, "persisted"},
		{controllertelemetry.Projected, "projected"}, {controllertelemetry.Skipped, "skipped"},
		{controllertelemetry.Failed, "failed"}, {255, "unknown"},
	}
	reasons := []struct {
		value controllertelemetry.Reason
		wire  string
	}{
		{controllertelemetry.ReasonUnknown, "unknown"}, {controllertelemetry.ImmediateClose, "immediate_close"},
		{controllertelemetry.ScheduledClose, "scheduled_close"}, {controllertelemetry.ManualSelection, "manual_selection"},
		{controllertelemetry.RecentSelectable, "recent_selectable"}, {controllertelemetry.DurableSelectable, "durable_selectable"},
		{controllertelemetry.NoSelectable, "no_selectable"}, {controllertelemetry.NewerSelection, "newer_selection"},
		{controllertelemetry.OtherNode, "other_node"}, {controllertelemetry.StaleSelection, "stale_selection"},
		{controllertelemetry.SessionCard, "session_card"}, {controllertelemetry.SessionList, "session_list"},
		{controllertelemetry.NativeSurface, "native_surface"}, {controllertelemetry.CloseFailed, "close_failed"},
		{controllertelemetry.InvalidCloseResult, "invalid_close_result"}, {controllertelemetry.LoadFailed, "load_failed"},
		{controllertelemetry.ListFailed, "list_failed"}, {controllertelemetry.PersistFailed, "persist_failed"},
		{controllertelemetry.ProjectionFailed, "projection_failed"}, {controllertelemetry.StoreUnavailable, "store_unavailable"},
		{controllertelemetry.Cancelled, "cancelled"}, {controllertelemetry.DeadlineExceeded, "deadline_exceeded"}, {255, "unknown"},
	}
	for i, reason := range reasons {
		event := telegramtrace.Controller(controllertelemetry.Event{
			Stage: stages[i%len(stages)].value, Reason: reason.value, Outcome: outcomes[i%len(outcomes)].value,
			SessionID: "private-payload /secret/file token=secret", TargetSessionID: "private-payload /secret/file token=secret",
		})
		if err := logger.Write(telegramtrace.Record(event, bytes.Repeat([]byte{42}, 32), uint64(i+1))); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-payload", "/secret/file", "token=secret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("identity leaked")
		}
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	if len(lines) != len(reasons) {
		t.Fatalf("records=%d", len(lines))
	}
	for i, line := range lines {
		var row safelog.Event
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		for field, expected := range map[string]string{
			"stage": stages[i%len(stages)].wire, "selection_reason": reasons[i].wire,
			"selection_outcome": outcomes[i%len(outcomes)].wire,
		} {
			if row.Fields[field] != expected {
				t.Errorf("row %d %s=%q want %q", i, field, row.Fields[field], expected)
			}
		}
		if row.Fields["session_ref"] == "" || row.Fields["session_ref"] != row.Fields["target_session_ref"] {
			t.Fatal("identity domain mismatch")
		}
	}
}
