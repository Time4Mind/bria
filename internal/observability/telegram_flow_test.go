package observability_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/coordinator"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

func TestTelegramFlowReadyIsPersistedBeforeConstructorReturns(t *testing.T) {
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
	raw, err := os.ReadFile(filepath.Join(dir, "service.jsonl"))
	if err != nil {
		t.Fatal("readiness not physically persisted:", err)
	}
	var ready safelog.Event
	if err := json.Unmarshal(raw, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "telegram.flow_ready" || ready.Fields["version"] != "3" || ready.Fields["run_ref"] == "" {
		t.Fatalf("readiness = %#v", ready)
	}
	observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "callback.accept"})
	observer.Close()
	rows, err := logger.Read(safelog.Detailed)
	if err != nil || len(rows) != 1 {
		t.Fatalf("flow records = %#v, %v", rows, err)
	}
	if rows[0].Fields["run_ref"] != ready.Fields["run_ref"] {
		t.Fatal("readiness and flow run refs differ")
	}
}

func blockedFlowObserver(t *testing.T) (*observability.TelegramFlowObserver, *safelog.Logger, func()) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir(), Now: func() time.Time {
		if calls.Add(1) == 2 {
			close(started)
			<-release
		}
		return time.Now().Add(time.Hour)
	}})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblock(); observer.Close() })
	observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "callback.accept"})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("writer never reached persistence")
	}
	return observer, logger, unblock
}

func TestTelegramFlowOwnsQueuedMetadataAndObservationTime(t *testing.T) {
	observer, logger, unblock := blockedFlowObserver(t)
	before := time.Now()
	observed := before.Add(-time.Minute).Round(0)
	for i := 0; i < 8; i++ {
		buttons := make([]string, 40)
		for j := range buttons {
			buttons[j] = "private-button-before-mutation"
		}
		event := telegramflow.TraceEvent{Stage: "callback.accept", CallbackID: buttons[0], ButtonIDs: buttons}
		if i%2 == 0 {
			event.Time = observed
		}
		observer.ObserveTelegramFlow(context.Background(), event)
		for j := range buttons {
			buttons[j] = "private-button-after-mutation"
		}
	}
	after := time.Now()
	unblock()
	observer.Close()
	rows, err := logger.Read(safelog.Detailed)
	if err != nil || len(rows) != 9 {
		t.Fatalf("rows=%d, %v", len(rows), err)
	}
	for i, row := range rows[1:] {
		if row.Fields["sequence"] != strconv.Itoa(i+2) {
			t.Error("sequence does not follow enqueue order")
		}
		buttons := strings.Split(row.Fields["button_refs"], ",")
		if len(buttons) != 32 || row.Fields["button_refs_truncated"] != "8" {
			t.Errorf("button bound/count lost: %#v", row.Fields)
		}
		for _, button := range buttons {
			if button != row.Fields["callback_ref"] {
				t.Error("queued metadata changed after Observe returned")
				break
			}
		}
		if i%2 == 0 {
			if !row.Time.Equal(observed) {
				t.Error("explicit observation time lost")
			}
		} else if row.Time.Before(before) || row.Time.After(after) {
			t.Errorf("zero time was not captured at observation: %s", row.Time)
		}
	}
}

func TestTelegramFlowConcurrentObserverDrainsInChannelOrder(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	var workers sync.WaitGroup
	for producer := 0; producer < 8; producer++ {
		workers.Add(1)
		go func(producer int) {
			defer workers.Done()
			buttons := []string{"private-button"}
			for n := 0; n < 32; n++ {
				buttons[0] = "private-button"
				observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "card.commit", CallbackID: buttons[0], ButtonIDs: buttons, HasPage: true, Page: n, Pages: producer})
				buttons[0] = "private-mutated-button"
			}
		}(producer)
	}
	workers.Wait()
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); observer.Close() }()
	}
	workers.Wait()
	observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "ignored.after.close"})
	rows, err := logger.Read(safelog.Detailed)
	if err != nil || len(rows) != 256 {
		t.Fatalf("drained rows=%d, %v", len(rows), err)
	}
	var next [8]int
	for i, row := range rows {
		if row.Fields["sequence"] != strconv.Itoa(i+1) {
			t.Error("persisted sequence is not channel order")
		}
		if row.Fields["callback_ref"] != row.Fields["button_refs"] {
			t.Error("metadata mutation escaped snapshot")
		}
		producer, err := strconv.Atoi(row.Fields["pages"])
		if err != nil || producer < 0 || producer >= len(next) {
			t.Fatal("invalid producer")
		}
		if row.Fields["page"] != strconv.Itoa(next[producer]) {
			t.Error("per-producer enqueue order lost")
		}
		next[producer]++
	}
}

func TestTelegramFlowDroppedEventsHaveRunAndOrderedSequence(t *testing.T) {
	observer, logger, unblock := blockedFlowObserver(t)
	for i := 0; i < 4200; i++ {
		observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "callback.accept"})
	}
	unblock()
	observer.Close()
	detailed, err := logger.Read(safelog.Detailed)
	if err != nil {
		t.Fatal(err)
	}
	service, err := logger.Read(safelog.Service)
	if err != nil || len(service) != 2 {
		t.Fatalf("service records=%d, %v", len(service), err)
	}
	ready, dropped := service[0], service[1]
	if ready.Type != "telegram.flow_ready" || dropped.Type != "telegram.flow_trace_dropped" || dropped.Fields["count"] != "104" || len(detailed) != 4097 {
		t.Fatalf("overflow not accounted: detailed=%d, service=%#v", len(detailed), service)
	}
	seen := map[string]bool{}
	for _, row := range append(detailed, service...) {
		seq := row.Fields["sequence"]
		if seen[seq] || seq == "" {
			t.Fatal("missing/duplicate sequence")
		}
		seen[seq] = true
		if row.Fields["run_ref"] != ready.Fields["run_ref"] {
			t.Fatal("dropped event lost run correlation")
		}
	}
	for i := 0; i < len(seen); i++ {
		if !seen[strconv.Itoa(i)] {
			t.Fatalf("sequence gap at %d", i)
		}
	}
}

func TestTelegramFlowReadyWriteFailureFailsConstruction(t *testing.T) {
	logger, err := safelog.Open(safelog.Options{Directory: t.TempDir(), MaxRecordBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if observer != nil {
		observer.Close()
	}
	if err == nil || observer != nil {
		t.Fatal("constructor succeeded without a persisted readiness record")
	}
}

func TestTelegramFlowWriteFailuresBecomeVisibleAfterRecovery(t *testing.T) {
	failed := make(chan struct{})
	var calls atomic.Int64
	dir := t.TempDir()
	logger, err := safelog.Open(safelog.Options{Directory: dir, Now: func() time.Time {
		switch calls.Add(1) {
		case 2:
			return time.Time{}
		case 3:
			close(failed)
			return time.Time{}
		default:
			return time.Now()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := observability.NewTelegramFlowObserver(logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(observer.Close)
	for i := 0; i < 2; i++ {
		observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "callback.accept"})
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("failure probe did not run")
	}
	observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "card.commit"})
	observer.ObserveTelegramFlow(context.Background(), telegramflow.TraceEvent{Stage: "card.commit"})
	observer.Close()
	rows, err := logger.Read(safelog.Detailed)
	if err != nil || len(rows) != 2 {
		t.Fatalf("recovered rows=%d, %v", len(rows), err)
	}
	if rows[0].Fields["failed_count"] != "2" || rows[0].Fields["sequence"] != "3" {
		t.Errorf("writer failure evidence lost: %#v", rows[0])
	}
	if _, ok := rows[1].Fields["failed_count"]; ok {
		t.Error("successful recovery did not reset failure count")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil || !strings.Contains(string(raw), `"failed_count":"2"`) {
		t.Fatalf("failure evidence not physically present: %v", err)
	}
}

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
