package acceptedrecovery_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/acceptedrecovery"
	"bria/internal/domain"
	"bria/internal/durableflow"
	"bria/internal/messagejournal"
	"bria/internal/recoverycomposition"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/storage"
)

type archiveResumeFunc func(context.Context, domain.SessionID) (domain.Session, error)

func (f archiveResumeFunc) Resume(ctx context.Context, id domain.SessionID) (domain.Session, error) {
	return f(ctx, id)
}

type recoveryRead func(context.Context, sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error)

func (f recoveryRead) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	return f(ctx, request)
}

type restoreFinal func(context.Context, domain.SessionID, string, string) error

func (f restoreFinal) RestoreAcceptedFinal(ctx context.Context, id domain.SessionID, message, final string) error {
	return f(ctx, id, message, final)
}

func TestGuardedArchiveResumePersistsLateFinalBeforeStartingExactSession(t *testing.T) {
	guardedArchiveResume(t, false)
}

func TestGuardedArchiveResumeRejectsBindingChangedDuringFinalPersistence(t *testing.T) {
	guardedArchiveResume(t, true)
}

func guardedArchiveResume(t *testing.T, mutateBinding bool) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	journalPath, statePath := filepath.Join(dir, "journal.json"), filepath.Join(dir, "state.json")
	journal, err := messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.EnqueueInput(ctx, "logical", "a", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.LeaseNextInput(ctx, "logical", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkInputAccepted(ctx, "logical", "a", "worker"); err != nil {
		t.Fatal(err)
	}
	state, err := storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	starting, err := domain.NewStartingSessionAt("logical", "intent", "computer", domain.ProviderCodex, "/work", time.Unix(1, 0), domain.SessionLifetimeNever)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = state.PutStartingIfAbsent(ctx, starting); err != nil {
		t.Fatal(err)
	}
	if err = state.SetCardPrompt(ctx, "logical", "a", "synthetic"); err != nil {
		t.Fatal(err)
	}
	binding := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "thread", Generation: 1}
	ready, err := starting.ReadyAt(binding, time.Unix(2, 0))
	if err != nil {
		t.Fatal(err)
	}
	closing, err := ready.BeginClose(time.Unix(3, 0))
	if err != nil {
		t.Fatal(err)
	}
	archived, err := closing.Archive(time.Unix(4, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Replace(ctx, starting, archived); err != nil {
		t.Fatal(err)
	}
	state, err = storage.OpenSessionStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err = messagejournal.Open(journalPath, messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	terminal := false
	history, err := recoverycomposition.NewReconciler(recoveryRead(func(_ context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
		if request.Binding != binding {
			t.Fatalf("recovery crossed retained binding: %+v", request)
		}
		turn := sessionruntime.ReconciledAcceptedTurn{MessageID: "a", Outcome: sessionruntime.AcceptedTurnUnknown, TurnID: "exact-turn"}
		if terminal {
			turn.Outcome, turn.Final = sessionruntime.AcceptedTurnCompleted, "Exact saved final"
		}
		return sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{turn}}, nil
	}), state)
	if err != nil {
		t.Fatal(err)
	}
	started := false
	guard := acceptedrecovery.GuardedArchivedResumer{Sessions: state, Reconciler: acceptedrecovery.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{domain.ProviderCodex: history}, FinalRestorer: state}, Base: archiveResumeFunc(func(ctx context.Context, id domain.SessionID) (domain.Session, error) {
		if started || id != "logical" {
			t.Fatal("duplicate or mismatched provider resume")
		}
		opened, err := storage.OpenSessionStore(statePath)
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := opened.LoadCardTranscript(ctx, id, true)
		if err != nil || len(blocks) != 2 || blocks[1].Text != "Exact saved final" {
			t.Fatalf("resume before physical final: %+v %v", blocks, err)
		}
		inputs, err := journal.Inputs(ctx, "logical")
		if err != nil || inputs[0].Phase != messagejournal.InputCompleted {
			t.Fatalf("resume before journal commit: %+v %v", inputs, err)
		}
		resuming, err := archived.BeginResume(time.Unix(5, 0))
		if err != nil {
			return domain.Session{}, err
		}
		next := binding
		next.Generation++
		resumed, err := resuming.ResumeReady(next, time.Unix(5, 0), domain.SessionLifetimeNever)
		if err == nil {
			err = state.Replace(ctx, archived, resumed)
		}
		started = err == nil
		return resumed, err
	})}
	if mutateBinding {
		guard.Reconciler.FinalRestorer = restoreFinal(func(ctx context.Context, id domain.SessionID, message, final string) error {
			if err := state.RestoreAcceptedFinal(ctx, id, message, final); err != nil {
				return err
			}
			snapshot := archived.Snapshot()
			snapshot.Binding.Generation++
			replaced, err := domain.RestoreSession(snapshot)
			if err != nil {
				return err
			}
			return state.Replace(ctx, archived, replaced)
		})
	}
	if _, err := guard.Resume(ctx, "logical"); !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || started {
		t.Fatalf("pending archive resumed: %v", err)
	}
	inputs, err := journal.Inputs(ctx, "logical")
	if err != nil || inputs[0].Phase != messagejournal.InputAccepted {
		t.Fatalf("acceptance lost: %+v %v", inputs, err)
	}
	terminal = true
	resumed, err := guard.Resume(ctx, "logical")
	if mutateBinding {
		if !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) || started {
			t.Fatalf("replaced binding unarchived: %+v %v", resumed, err)
		}
		current, loadErr := state.Load(ctx, "logical")
		currentBinding, _ := current.Binding()
		if loadErr != nil || current.Status() != domain.SessionArchived || currentBinding.Generation != binding.Generation+1 {
			t.Fatalf("replacement overwritten: %+v %v", current, loadErr)
		}
		return
	}
	if err != nil || !started || resumed.Status() != domain.SessionReady {
		t.Fatalf("proven terminal did not resume: %+v %v", resumed, err)
	}
	if _, err := guard.Resume(ctx, "logical"); !errors.Is(err, sessionsupervisor.ErrReconciliationRequired) {
		t.Fatalf("nonarchived resume repeated: %v", err)
	}
}
