package observability_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramtrace"
)

func TestControllerChainPersistsAndJoinsActualCardTrace(t *testing.T) {
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
	hook, ok := any(observer).(controllertelemetry.Observer)
	if !ok {
		t.Fatal("flow observer does not accept controller telemetry")
	}
	ctx := controllertelemetry.WithOperation(context.Background(), "private-close-operation")
	observed := time.Now().UTC().Add(-time.Minute).Round(0)
	event := controllertelemetry.Event{
		Time: observed, SessionID: "private-closed", NodeID: "private-node",
		PreviousSessionID: "private-closed", TargetSessionID: "private-next",
	}
	event.Stage, event.Outcome, event.Reason = controllertelemetry.ArchiveOutcome, controllertelemetry.Archived, controllertelemetry.ScheduledClose
	hook.ObserveControllerEvent(ctx, event)
	event.Stage, event.Outcome, event.Reason = controllertelemetry.FallbackChoice, controllertelemetry.Selected, controllertelemetry.DurableSelectable
	event.CandidatesKnown, event.CandidateCount = true, 2
	hook.ObserveControllerEvent(ctx, event)
	event.Stage, event.Outcome = controllertelemetry.SelectionPersist, controllertelemetry.Persisted
	event.CandidatesKnown = false
	hook.ObserveControllerEvent(ctx, event)
	event.Stage, event.Outcome, event.Reason = controllertelemetry.ProjectedTarget, controllertelemetry.Projected, controllertelemetry.SessionCard
	event.OperationID, event.ParentOperationID = "private-refresh-operation", controllertelemetry.Operation(ctx)
	hook.ObserveControllerEvent(ctx, event)
	observer.ObserveTelegramFlow(ctx, telegramtrace.Event{
		Stage: "card.edit_started", OperationID: "private-refresh-operation", SessionID: "private-next", Result: "started",
	})
	observer.Close()
	rows := controllerRows(t, dir, "detailed.jsonl")
	if len(rows) != 5 {
		t.Fatalf("rows=%d", len(rows))
	}
	for i, stage := range []string{"session.archive_outcome", "selection.fallback", "selection.persist", "selection.project", "card.edit_started"} {
		if rows[i].Fields["stage"] != stage {
			t.Fatalf("row %d=%v", i, rows[i])
		}
		if rows[i].Fields["run_ref"] != rows[0].Fields["run_ref"] {
			t.Fatal("different run references")
		}
	}
	if rows[0].EntityID == "" || rows[0].EntityID != rows[1].EntityID || rows[0].EntityID != rows[2].EntityID || rows[3].Fields["parent_operation_ref"] != rows[0].EntityID || rows[3].EntityID != rows[4].EntityID || rows[3].EntityID == rows[0].EntityID {
		t.Fatal("operation chain lost")
	}
	if rows[0].Fields["session_ref"] != rows[1].Fields["previous_session_ref"] || rows[3].Fields["target_session_ref"] != rows[4].Fields["session_ref"] {
		t.Fatal("session HMAC domain mismatch")
	}
	if rows[1].Fields["candidate_count"] != "2" || rows[2].Fields["candidate_count"] != "" {
		t.Fatal("unknown and known candidate counts conflated")
	}
	if !rows[0].Time.Equal(observed) || rows[0].Fields["sequence"] != "1" || rows[4].Fields["sequence"] != "5" {
		t.Fatal("capture time or shared sequence lost")
	}
	for i, outcome := range []string{"archived", "selected", "persisted", "projected"} {
		if rows[i].Fields["selection_outcome"] != outcome || rows[i].Result != outcome || rows[i].ErrorCategory != "" || rows[i].Fields["reason"] != "" {
			t.Fatalf("outcome incorrectly treated as callback error: %v", rows[i])
		}
	}
	ready := controllerRows(t, dir, "service.jsonl")
	if len(ready) != 1 || ready[0].Fields["version"] != "3" || ready[0].Fields["run_ref"] != rows[0].Fields["run_ref"] {
		t.Fatalf("schema/run=%v", ready)
	}
}

func controllerRows(t *testing.T, dir, name string) []safelog.Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-") {
		t.Fatal("private identity/payload leaked")
	}
	var rows []safelog.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var row safelog.Event
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestControllerUnknownsAndEmptySelectionPersistWithoutInventedFacts(t *testing.T) {
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
	observer.ObserveControllerEvent(context.Background(), controllertelemetry.Event{
		Stage: 255, Reason: 255, Outcome: 255, CandidateCount: 19,
	})
	observer.ObserveControllerEvent(context.Background(), controllertelemetry.Event{
		Stage: controllertelemetry.FallbackChoice, Reason: controllertelemetry.NoSelectable,
		Outcome: controllertelemetry.Cleared, CandidatesKnown: true,
	})
	observer.Close()
	rows := controllerRows(t, dir, "detailed.jsonl")
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	for _, field := range []string{"stage", "selection_reason", "selection_outcome"} {
		if rows[0].Fields[field] != "unknown" {
			t.Fatalf("%s not closed: %v", field, rows[0])
		}
	}
	if rows[0].Result != "unknown" || rows[0].EntityID != "" || rows[0].Fields["candidate_count"] != "" {
		t.Fatal("unknown facts invented")
	}
	if rows[1].Fields["candidate_count"] != "0" || rows[1].Fields["selection_outcome"] != "cleared" {
		t.Fatal("empty selection not explicit")
	}
	for _, row := range rows {
		for _, field := range []string{"session_ref", "target_session_ref", "previous_session_ref", "node_ref", "parent_operation_ref", "reason"} {
			if _, exists := row.Fields[field]; exists {
				t.Fatalf("absent identity/error invented: %s", field)
			}
		}
	}
}

func TestControllerAndCardObserversShareQueueDuringConcurrentClose(t *testing.T) {
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
	ctx := context.Background()
	var producers sync.WaitGroup
	for i := 0; i < 16; i++ {
		producers.Add(1)
		go func(i int) {
			defer producers.Done()
			op := "private-operation-" + strconv.Itoa(i)
			observer.ObserveControllerEvent(ctx, controllertelemetry.Event{OperationID: op, Stage: controllertelemetry.SelectionPersist, Outcome: controllertelemetry.Persisted})
			observer.ObserveTelegramFlow(ctx, telegramtrace.Event{OperationID: op, Stage: "state.commit"})
		}(i)
	}
	producers.Wait()
	var closing sync.WaitGroup
	for i := 0; i < 16; i++ {
		closing.Add(1)
		go func() {
			defer closing.Done()
			observer.ObserveControllerEvent(ctx, controllertelemetry.Event{Stage: controllertelemetry.ProjectedTarget, Outcome: controllertelemetry.Projected})
			observer.Close()
		}()
	}
	closing.Wait()
	observer.ObserveControllerEvent(ctx, controllertelemetry.Event{SessionID: "private-after-close"})
	rows := controllerRows(t, dir, "detailed.jsonl")
	if len(rows) < 32 || len(rows) > 48 {
		t.Fatalf("flush rows=%d", len(rows))
	}
	pairs := map[string][]string{}
	for i, row := range rows {
		if row.Fields["sequence"] != strconv.Itoa(i+1) {
			t.Fatal("shared sequence lost")
		}
		if row.EntityID != "" {
			pairs[row.EntityID] = append(pairs[row.EntityID], row.Fields["stage"])
		}
	}
	if len(pairs) != 16 {
		t.Fatalf("pairs=%d", len(pairs))
	}
	for _, stages := range pairs {
		if strings.Join(stages, ",") != "selection.persist,state.commit" {
			t.Fatalf("enqueue order=%v", stages)
		}
	}
}
