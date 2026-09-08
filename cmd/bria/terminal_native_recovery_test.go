package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/nativeacceptance"
	"bria/internal/nativereceiptstore"
	"bria/internal/recoverycomposition"
	"bria/internal/recoveryruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
)

func TestNativeTerminalProofReopensAndUnblocksOnlyNextInput(t *testing.T) {
	for _, prior := range []messagejournal.InputPhase{messagejournal.InputAccepted, messagejournal.InputUnknown, messagejournal.InputFailed} {
		for _, conflict := range []bool{false, true} {
			name := string(prior) + "/exact"
			if conflict {
				name = string(prior) + "/conflicting-complete"
			}
			t.Run(name, func(t *testing.T) { nativeTerminalRecovery(t, prior, conflict) })
		}
	}
}

// Every evidence source and consumer is real: receipt file, partial native
// JSONL, fresh native reader, recovery bridge, durable flow, journal and history.
func nativeTerminalRecovery(t *testing.T, prior messagejournal.InputPhase, conflict bool) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	journalPath, statePath := filepath.Join(dir, "journal.json"), filepath.Join(dir, "state.json")
	const logical domain.SessionID = "11111111-1111-4111-9111-111111111111"
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "00000000-0000-4000-8000-000000000077", Generation: 1}
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSession(logical, "native-recovery-intent", "local", domain.ProviderCodex, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := state.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.CompareAndSwap(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	if err := state.SetCardPrompt(ctx, logical, "native-A", "Synthetic accepted A"); err != nil {
		t.Fatal(err)
	}
	baselineHistory, err := state.LoadCardTranscript(ctx, logical, true)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"native-A", "native-B"} {
		if _, _, err := journal.EnqueueInput(ctx, string(logical), message, []byte("synthetic "+message)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := journal.LeaseNextInput(ctx, string(logical), "initial", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkInputAccepted(ctx, string(logical), "native-A", "initial"); err != nil {
		t.Fatal(err)
	}
	if prior == messagejournal.InputUnknown {
		_, err = journal.MarkInputUnknown(ctx, string(logical), "native-A")
	} else if prior == messagejournal.InputFailed {
		_, err = journal.FailInput(ctx, string(logical), "native-A")
	}
	if err != nil {
		t.Fatal(err)
	}
	root, transcriptRoot := filepath.Join(dir, "receipts"), filepath.Join(dir, "sessions")
	savedOutcome := "unknown"
	if prior == messagejournal.InputFailed {
		savedOutcome = "failed"
	}
	if err := nativereceiptstore.Write(root, nativeacceptance.Document{SessionID: binding.SessionID, Receipts: map[string]string{"native-A": savedOutcome}, TurnIDs: map[string]string{"native-A": "exact-turn-A"}}); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(root, binding.SessionID+".json")
	receiptBytes, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(transcriptRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transcriptRoot, "rollout-"+binding.SessionID+".jsonl")
	header, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]string{"id": binding.SessionID, "cwd": dir}})
	if err != nil {
		t.Fatal(err)
	}
	data := string(header) + "\n" + `{"type":"turn_context","payload":{"turn_id":"exact-turn-A"}}` + "\n" + `{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"exact-turn-A"}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 4; pass++ {
		if pass == 2 {
			suffix := "\n"
			if conflict {
				suffix += `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"exact-turn-A"}}` + "\n"
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.WriteString(suffix)
			if err := errors.Join(writeErr, file.Close()); err != nil {
				t.Fatal(err)
			}
			data += suffix
		}
		journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		state, err = storage.OpenSessionStore(statePath)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := recoveryruntime.NewNativeWithTranscriptRoot(root, domain.ProviderCodex, transcriptRoot)
		if err != nil {
			t.Fatal(err)
		}
		history, err := recoverycomposition.NewReconciler(reader, state)
		if err != nil {
			t.Fatal(err)
		}
		turn, finalProven, err := history.LookupFinal(ctx, logical, binding, "native-A")
		wantProof := pass >= 2 && !conflict
		if err != nil || turn.MessageID != "native-A" || turn.TurnID != "exact-turn-A" || turn.TerminalFailureProven != wantProof || finalProven || turn.Final != "" {
			t.Fatalf("pass %d exact native proof=%+v finalProven=%v err=%v wantFailureProof=%v", pass, turn, finalProven, err, wantProof)
		}
		flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "recovery", LeaseDuration: time.Minute, Now: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		reconciler := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: state}
		if _, err := reconciler.ReconcileAcceptedTurns(ctx, logical, binding); err != nil {
			t.Fatalf("pass %d physical reconciliation: %v", pass, err)
		}
		physical := terminalJournalInputs(t, ctx, journalPath, logical)
		wantPhase := messagejournal.InputUnknown
		if prior == messagejournal.InputFailed {
			wantPhase = messagejournal.InputFailed
		}
		if wantProof {
			wantPhase = messagejournal.InputTerminalFailed
		}
		if len(physical) != 2 || physical[0].MessageID != "native-A" || physical[0].Sequence != 1 || physical[0].Phase != wantPhase || physical[1].MessageID != "native-B" || physical[1].Sequence != 2 || physical[1].Phase != messagejournal.InputPending {
			t.Fatalf("pass %d physical journal=%+v want A=%s B=pending", pass, physical, wantPhase)
		}
		if !wantProof {
			if next, err := journal.LeaseNextInput(ctx, string(logical), "blocked", time.Unix(100, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
				t.Fatalf("pass %d incomplete/conflicting proof leased %+v: %v", pass, next, err)
			}
		}
		reopened, err := storage.OpenSessionStore(statePath)
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := reopened.LoadCardTranscript(ctx, logical, true)
		if err != nil || !reflect.DeepEqual(blocks, baselineHistory) {
			t.Fatalf("pass %d failure proof invented/duplicated history: %+v err=%v", pass, blocks, err)
		}
	}
	if !conflict {
		next, err := journal.LeaseNextInput(ctx, string(logical), "next-worker", time.Unix(100, 0), time.Minute)
		if err != nil || next.MessageID != "native-B" || next.Sequence != 2 || string(next.Payload) != "synthetic native-B" {
			t.Fatalf("exact proof must lease only B: %+v err=%v", next, err)
		}
		if _, err := journal.LeaseNextInput(ctx, string(logical), "duplicate-worker", time.Unix(101, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
			t.Fatalf("concurrent B lease: %v", err)
		}
		if _, err := journal.MarkInputAccepted(ctx, string(logical), "native-B", "next-worker"); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.CompleteInput(ctx, string(logical), "native-B"); err != nil {
			t.Fatal(err)
		}
		journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.RetryInput(ctx, string(logical), "native-A"); !errors.Is(err, messagejournal.ErrInvalidTransition) {
			t.Fatalf("terminal A is replayable after reopen: %v", err)
		}
		if _, err := journal.LeaseNextInput(ctx, string(logical), "after-reopen", time.Unix(1000, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
			t.Fatalf("old input replay after B completion: %v", err)
		}
	}
	for source, want := range map[string]string{path: data, receiptPath: string(receiptBytes)} {
		got, err := os.ReadFile(source)
		if err != nil || string(got) != want {
			t.Fatalf("reconciliation changed physical native evidence: %v", err)
		}
	}
}
