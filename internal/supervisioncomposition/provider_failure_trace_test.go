package supervisioncomposition_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/controllertelemetry"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramtrace"
)

// Validate the physical adapter boundary: the provider hook is emitted with the
// accepted turn's operation, recovery is produced by the real manager, and typed
// projection/delivery events use the same observer and session HMAC domain.
// Root runtime/controller hook placement has its own integration acceptance.
func TestProviderFailureJoinsRecoveryProjectionAndDeliveryJSONL(t *testing.T) {
	for _, class := range []string{"native_transcript_record_too_large", "native_transcript_read_limit", "native_transcript_malformed", "native_transcript_binding_invalid", "private-unclassified-error"} {
		t.Run(controllertelemetry.RuntimeFailureReason(class).String(), func(t *testing.T) {
			running, _ := runningSession(t)
			awaiting, err := running.AwaitRecovery()
			if err != nil {
				t.Fatal(err)
			}
			store := &memoryStore{session: awaiting}
			runtime := &runtimeStub{}
			manager := recoveryManager(t, store, runtime, runtime, &recoveryReconciler{unknown: true})
			dir := t.TempDir()
			logger, err := safelog.Open(safelog.Options{Directory: dir})
			if err != nil {
				t.Fatal(err)
			}
			observer, err := observability.NewTelegramFlowObserver(logger)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Close()
			manager.SetObserver(observer)
			ctx := controllertelemetry.WithOperation(context.Background(), "private-accepted-turn")
			observer.ObserveControllerEvent(ctx, controllertelemetry.Event{
				Stage: controllertelemetry.ProviderFailure, Outcome: controllertelemetry.Failed,
				Reason: controllertelemetry.RuntimeFailureReason(class), SessionID: string(running.ID()), NodeID: string(running.ComputerID()),
			})
			// Automatic/manual recovery has its own initiating operation. A shared
			// session reference must join it without falsely equating operation IDs.
			recoveryCtx := controllertelemetry.WithOperation(context.Background(), "private-recovery")
			if _, err := manager.RecoverSession(recoveryCtx, running.ID()); err == nil {
				t.Fatal("unknown provider outcome unexpectedly recovered")
			}
			observer.ObserveControllerEvent(recoveryCtx, controllertelemetry.Event{
				Stage: controllertelemetry.ProjectedTarget, Outcome: controllertelemetry.Projected, Reason: controllertelemetry.SessionCard,
				SessionID: string(running.ID()), TargetSessionID: string(running.ID()), NodeID: string(running.ComputerID()),
				OperationID: "private-card-refresh", ParentOperationID: "private-recovery",
			})
			observer.ObserveTelegramFlow(ctx, telegramtrace.Event{Stage: "card.edit_started", Result: "started", OperationID: "private-card-refresh", SessionID: string(running.ID())})
			observer.ObserveTelegramFlow(ctx, telegramtrace.Event{Stage: "transport.edit", Result: "confirmed", OperationID: "private-card-refresh", SessionID: string(running.ID())})
			observer.Close()
			data, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"private-", string(running.ID()), running.Workdir(), "thread-1"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("failure chain leaked payload or raw identity")
				}
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != 5 {
				t.Fatalf("physical chain has %d rows, want 5", len(lines))
			}
			rows := make([]safelog.Event, len(lines))
			for i, line := range lines {
				if err := json.Unmarshal([]byte(line), &rows[i]); err != nil {
					t.Fatal(err)
				}
			}
			for i, stage := range []string{"session.provider_failure", "session.recovery_outcome", "selection.project", "card.edit_started", "transport.edit"} {
				if rows[i].Fields["stage"] != stage {
					t.Fatalf("chain stage %d was lost", i)
				}
				if rows[i].Fields["session_ref"] == "" || rows[i].Fields["session_ref"] != rows[0].Fields["session_ref"] || rows[i].Fields["run_ref"] != rows[0].Fields["run_ref"] {
					t.Fatal("runtime failure cannot join recovery/UI in shared session/run domain")
				}
			}
			if rows[0].Fields["selection_reason"] != controllertelemetry.RuntimeFailureReason(class).String() || rows[0].Result != "failed" || rows[1].Result != "recovery_unknown" {
				t.Fatal("failure/recovery classification was lost")
			}
			if rows[0].EntityID == "" || rows[1].EntityID == "" || rows[0].EntityID == rows[1].EntityID {
				t.Fatal("distinct runtime and recovery operation identities were conflated")
			}
			if rows[2].Fields["parent_operation_ref"] != rows[1].EntityID || rows[2].EntityID != rows[3].EntityID || rows[2].EntityID != rows[4].EntityID || rows[2].Fields["target_session_ref"] != rows[3].Fields["session_ref"] {
				t.Fatal("projection-to-delivery correlation was lost")
			}
		})
	}
}
