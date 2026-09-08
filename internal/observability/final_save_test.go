package observability_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"bria/internal/controllertelemetry"
	"bria/internal/observability"
	"bria/internal/safelog"
)

func TestFinalSaveFailuresAndRecoveryReachSafePhysicalLog(t *testing.T) {
	// Synthetic private identifiers exercise every correlation field, not a live payload.
	const private = "private-final token=fake-secret /private/session transcript-text"
	var previousRun, previousSession string
	for run := 0; run < 2; run++ {
		dir := t.TempDir()
		logger, err := safelog.Open(safelog.Options{Directory: dir})
		if err != nil {
			t.Fatal(err)
		}
		observer, err := observability.NewTelegramFlowObserver(logger)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(observer.Close)
		ctx := controllertelemetry.WithOperation(context.Background(), private)
		for _, outcome := range []controllertelemetry.Outcome{controllertelemetry.Failed, controllertelemetry.Failed, controllertelemetry.Failed, controllertelemetry.Persisted} {
			observer.ObserveControllerEvent(ctx, controllertelemetry.Event{
				Stage: controllertelemetry.FinalSave, Reason: controllertelemetry.PersistFailed, Outcome: outcome,
				SessionID: private, NodeID: private, ParentOperationID: private,
				PreviousSessionID: private, TargetSessionID: private,
			})
		}
		observer.Close()
		raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{private, "private-final", "fake-secret", "/private/session", "transcript-text"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("raw private identifier escaped into physical log")
			}
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		if len(lines) != 4 {
			t.Fatalf("failure/recovery records = %d, want 4", len(lines))
		}
		var first safelog.Event
		for i, line := range lines {
			var row safelog.Event
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatal(err)
			}
			wantOutcome := "failed"
			if i == 3 {
				wantOutcome = "persisted"
			}
			if row.Type != "telegram.flow_stage" || row.Fields["stage"] != "session.final_save" || row.Fields["selection_reason"] != "persist_failed" || row.Fields["selection_outcome"] != wantOutcome || row.Result != wantOutcome {
				t.Fatalf("attempt %d lost final-save classification: %#v", i, row)
			}
			for _, ref := range []string{row.EntityID, row.Fields["parent_operation_ref"], row.Fields["session_ref"], row.Fields["previous_session_ref"], row.Fields["target_session_ref"], row.Fields["node_ref"], row.Fields["run_ref"]} {
				if !regexp.MustCompile(`^c_[0-9a-f]{64}$`).MatchString(ref) {
					t.Fatal("missing or malformed keyed correlation reference")
				}
			}
			if row.EntityID != row.Fields["parent_operation_ref"] || row.Fields["session_ref"] != row.Fields["target_session_ref"] || row.Fields["session_ref"] != row.Fields["previous_session_ref"] {
				t.Fatal("same-domain identity correlation lost")
			}
			if row.EntityID == row.Fields["session_ref"] || row.Fields["session_ref"] == row.Fields["node_ref"] || row.EntityID == row.Fields["node_ref"] {
				t.Fatal("identity HMAC domains not separated")
			}
			if i == 0 {
				first = row
			} else if row.EntityID != first.EntityID || row.Fields["session_ref"] != first.Fields["session_ref"] || row.Fields["run_ref"] != first.Fields["run_ref"] {
				t.Fatal("retry/recovery exact correlation changed")
			}
		}
		if run > 0 && (first.Fields["run_ref"] == previousRun || first.Fields["session_ref"] == previousSession) {
			t.Fatal("keyed references reused across observer runs")
		}
		previousRun, previousSession = first.Fields["run_ref"], first.Fields["session_ref"]
	}
}
