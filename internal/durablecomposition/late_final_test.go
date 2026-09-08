package durablecomposition_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestUnknownReopenRestoresOnlyLateExactFinalWithoutReplay(t *testing.T) {
	lateFinalRecovery(t, false)
}

func TestCorrelatedCompletedReceiptWaitsForPartialTailBeforeFinalCommit(t *testing.T) {
	lateFinalRecovery(t, true)
}

func lateFinalRecovery(t *testing.T, partialCompleted bool) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	journalPath, statePath := filepath.Join(dir, "journal.json"), filepath.Join(dir, "state.json")
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "logical", "m", []byte("Synthetic prompt")); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(200, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputAccepted(ctx, "logical", "m", "worker"); err != nil {
		t.Fatal(err)
	}
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSessionAt("logical", "intent", "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err = state.SetCardPrompt(ctx, "logical", "m", "Synthetic prompt"); err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "00000000-0000-4000-8000-000000000077", Generation: 1}
	session, err = session.ReadyAt(binding, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	root, transcript := filepath.Join(dir, "receipts"), filepath.Join(dir, "sessions")
	outcome := "unknown"
	if partialCompleted {
		outcome = "completed"
	}
	if err = nativereceiptstore.Write(root, nativeacceptance.Document{SessionID: binding.SessionID, Receipts: map[string]string{"m": outcome}, TurnIDs: map[string]string{"m": "turn-1"}}); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(transcript, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(transcript, "rollout-"+binding.SessionID+".jsonl")
	header := `{"type":"session_meta","payload":{"id":"` + binding.SessionID + `","cwd":"/work"}}` + "\n" + `{"type":"turn_context","payload":{"turn_id":"turn-1"}}` + "\n"
	terminal := `{"type":"event_msg","payload":{"type":"agent_message","phase":"final","message":"Late exact final"}}` + "\n" + `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1"}}` + "\n"
	initial, suffix := header, terminal
	if partialCompleted {
		initial, suffix = header+terminal+`{"type":"ignored"}`, "\n"
	}
	if err = os.WriteFile(path, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 4; pass++ {
		if pass == 2 {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := file.WriteString(suffix)
			if err = errors.Join(writeErr, file.Close()); err != nil {
				t.Fatal(err)
			}
		}
		journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		state, err = storage.OpenSessionStore(statePath)
		if err != nil {
			t.Fatal(err)
		}
		flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return time.Unix(210, 0) }})
		if err != nil {
			t.Fatal(err)
		}
		reader, err := recoveryruntime.NewNativeWithTranscriptRoot(root, domain.ProviderCodex, transcript)
		if err != nil {
			t.Fatal(err)
		}
		history, err := recoverycomposition.NewReconciler(reader, recoveryLoad{session})
		if err != nil {
			t.Fatal(err)
		}
		r := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: state}
		got, err := r.ReconcileAcceptedTurns(ctx, "logical", binding)
		if err != nil && !(partialCompleted && pass < 2) {
			t.Fatal(err)
		}
		if pass < 2 && (len(got.Turns) != 1 || got.Turns[0].Outcome != sessionsupervisor.AcceptedTurnUnknown) {
			t.Fatalf("pass %d unknown disappeared after reopen: %#v", pass, got)
		}
		inputs, err := journal.Inputs(ctx, "logical")
		if err != nil || len(inputs) != 1 {
			t.Fatalf("inputs: %#v %v", inputs, err)
		}
		wantPhase, wantBlocks := messagejournal.InputUnknown, 1
		if pass >= 2 {
			wantPhase, wantBlocks = messagejournal.InputCompleted, 2
		}
		if inputs[0].Phase != wantPhase {
			t.Fatalf("pass %d phase=%s", pass, inputs[0].Phase)
		}
		reopened, err := storage.OpenSessionStore(statePath)
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := reopened.LoadCardTranscript(ctx, "logical", true)
		if err != nil || len(blocks) != wantBlocks || pass >= 2 && (blocks[1].Kind != "final" || blocks[1].Text != "Late exact final") {
			t.Fatalf("pass %d premature/missing/duplicate final: %#v %v", pass, blocks, err)
		}
		if _, err = journal.LeaseNextInput(ctx, "logical", "other", time.Unix(999, 0), time.Minute); !errors.Is(err, messagejournal.ErrNoAvailable) {
			t.Fatalf("unknown prompt replayable: %v", err)
		}
	}
}
