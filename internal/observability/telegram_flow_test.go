package observability_test

import (
	"context"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

func TestTelegramFlowObserverFlushesTimestampedStageWithoutPayload(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if err != nil {
		t.Fatal(err)
	}
	observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{
		Stage: "controller.callback", OperationID: "status:42", UpdateID: 42, UpdateKind: coordinator.UpdateCallback,
		Action: telegramui.ActionMenuNodes, Effect: telegrampipeline.EffectShowStatus, Result: "completed", Duration: 125 * time.Millisecond,
	})
	observer.Close()
	records, err := logger.Read(safelog.Detailed)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v, %v", records, err)
	}
	record := records[0]
	if record.Time.IsZero() || record.Type != "telegram.flow_stage" || record.EntityID == "status:42" || record.Fields["stage"] != "controller.callback" || record.Fields["duration_ms"] != "125" || record.Fields["action"] != string(telegramui.ActionMenuNodes) {
		t.Fatalf("trace record = %#v", record)
	}
}
