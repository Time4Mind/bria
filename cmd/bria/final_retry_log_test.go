package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/observability"
	"bria/internal/safelog"
	"bria/internal/telegramcontroller"
)

type finalRetryLogState struct {
	mu     sync.Mutex
	failed map[string]bool
}

func (*finalRetryLogState) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (s *finalRetryLogState) RestoreAcceptedFinal(_ context.Context, _ domain.SessionID, messageID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.failed[messageID] {
		s.failed[messageID] = true
		return errors.New("PRIVATE_STORAGE_ERROR_SENTINEL")
	}
	return nil
}

func TestFinalRetryControllerPhysicalLogKeepsExactMessageCorrelation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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
	store, sessions := acceptedProofFixture(t)
	ready := sessions[0]
	state := &finalRetryLogState{failed: make(map[string]bool)}
	c, err := telegramcontroller.New(7, 42, "local", archiveCreator{}, store, retentionSubmitter{}, archiveNotifier(nil), telegramcontroller.Options{Recovered: sessions, UIState: state, ControllerObserver: observer})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
	for i, messageID := range []string{"PRIVATE_MESSAGE_A", "PRIVATE_MESSAGE_B"} {
		// A separate public observation supplies the expected same-domain HMAC
		// for this exact identity without reproducing the hashing implementation.
		observer.ObserveControllerEvent(ctx, controllertelemetry.Event{Stage: controllertelemetry.SelectionPersist, Outcome: controllertelemetry.Persisted, OperationID: messageID + ":final-save", ParentOperationID: messageID, SessionID: string(ready.ID())})
		receipt, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: messageID, Sequence: uint64(i + 1), Payload: []byte("PRIVATE_PROMPT_SENTINEL")}, telegramcontroller.DurableInputCallbacks{
			OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
			OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
				completed <- receipt
				return nil
			},
		})
		if err != nil || !receipt.Accepted {
			t.Fatalf("input %d not accepted: %v", i, err)
		}
		select {
		case receipt := <-completed:
			if receipt.Completion != telegramcontroller.DurableInputSucceeded || receipt.MessageID != messageID {
				t.Fatal("exact input did not finish after persistence retry")
			}
		case <-ctx.Done():
			t.Fatal("final retry did not settle")
		}
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	observer.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "detailed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"PRIVATE_", "RETENTION_FINAL", string(ready.ID()), ready.Workdir()} {
		if strings.Contains(string(raw), private) {
			t.Fatal("physical log contains a raw identifier, prompt, final or path")
		}
	}
	var references, parentRefs, sessionRefs []string
	var finals []safelog.Event
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var row safelog.Event
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		switch row.Fields["stage"] {
		case "selection.persist":
			references = append(references, row.EntityID)
			parentRefs = append(parentRefs, row.Fields["parent_operation_ref"])
			sessionRefs = append(sessionRefs, row.Fields["session_ref"])
		case "session.final_save":
			finals = append(finals, row)
		case "session.provider_failure":
			t.Fatal("final persistence fault misclassified as provider failure")
		}
	}
	if len(references) != 2 || len(finals) != 4 {
		t.Fatalf("reference/final-save records = %d/%d, want 2/4", len(references), len(finals))
	}
	if references[0] == references[1] || parentRefs[0] == parentRefs[1] {
		t.Fatal("distinct messages share an operation reference")
	}
	for i, row := range finals {
		if !regexp.MustCompile(`^c_[0-9a-f]{64}$`).MatchString(row.EntityID) || row.EntityID != references[i/2] {
			t.Errorf("final-save record %d lost exact message-correlated ref: %q", i, row.EntityID)
		}
		if !regexp.MustCompile(`^c_[0-9a-f]{64}$`).MatchString(row.Fields["parent_operation_ref"]) || row.Fields["parent_operation_ref"] != parentRefs[i/2] || row.EntityID == row.Fields["parent_operation_ref"] {
			t.Errorf("final-save record %d lost exact parent-message ref", i)
		}
		if row.Fields["session_ref"] != sessionRefs[i/2] || row.Error != "" {
			t.Fatal("session correlation lost or raw error retained")
		}
		wantOutcome, wantReason := "failed", "persist_failed"
		if i%2 == 1 {
			wantOutcome, wantReason = "persisted", "unknown"
		}
		if row.Result != wantOutcome || row.Fields["selection_outcome"] != wantOutcome || row.Fields["selection_reason"] != wantReason {
			t.Fatalf("final-save record %d has wrong safe outcome/reason", i)
		}
	}
}
