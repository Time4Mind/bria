package backupsource_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/backup"
	"bria/internal/backupflow"
	"bria/internal/backupruntime"
	"bria/internal/backupsource"
	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
)

func TestReopenerValidatesAndAtomicallyReopensTypedStores(t *testing.T) {
	liveRoot := restoredSemanticRoot(t)
	target := &restoreTarget{transaction: &restoreTransaction{receipt: backupsource.RestoreReceipt{ReceiptID: "typed-stores-reopened", Fingerprint: "verified-fingerprint"}}}
	reopener, err := backupsource.NewReopener(backupsource.ReopenerOptions{
		Target: target,
		Limits: backupsource.RestoreLimits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("NewReopener: %v", err)
	}

	receipt, err := reopener.Reopen(context.Background(), liveRoot, "verified-fingerprint")
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if receipt != "typed-stores-reopened" {
		t.Fatalf("receipt = %q", receipt)
	}
	if !reflect.DeepEqual(target.transaction.events, []string{"validate", "commit", "reopen"}) {
		t.Fatalf("events = %v", target.transaction.events)
	}
	if target.state.Fingerprint != "verified-fingerprint" || len(target.state.Sessions) != 1 || target.state.Sessions[0].ID != "session-1" {
		t.Fatalf("restored sessions = %#v", target.state)
	}
	if len(target.state.Undelivered.Sessions) != 1 || len(target.state.Undelivered.Sessions[0].Records) != 2 {
		t.Fatalf("restored undelivered = %#v", target.state.Undelivered)
	}
	records := target.state.Undelivered.Sessions[0].Records
	if records[0].SourceSequence != 1 || records[1].SourceSequence != 3 {
		t.Fatalf("source ordering = %#v", records)
	}
	if len(target.state.Histories) != 1 || target.state.Histories[0].Provider != domain.ProviderCodex || target.state.Histories[0].SessionID != "session-1" {
		t.Fatalf("restored histories = %#v", target.state.Histories)
	}
	if target.state.HarnessRoot != filepath.Join(liveRoot, "harness") {
		t.Fatalf("HarnessRoot = %q", target.state.HarnessRoot)
	}
}

func TestReopenerAllowsCanonicalStateWithNoSessionHistory(t *testing.T) {
	target := &restoreTarget{transaction: &restoreTransaction{}}
	if _, err := backupsource.NewReopener(backupsource.ReopenerOptions{
		Target: target,
		Limits: backupsource.RestoreLimits{MaxFiles: 8, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	}); err != nil {
		t.Fatalf("NewReopener for eight mandatory files: %v", err)
	}
}

func TestReopenerRollsBackCommittedStoresWhenReopenFails(t *testing.T) {
	liveRoot := restoredSemanticRoot(t)
	reopenFailure := errors.New("store reopen failed")
	target := &restoreTarget{transaction: &restoreTransaction{reopenErr: reopenFailure}}
	reopener, err := backupsource.NewReopener(backupsource.ReopenerOptions{
		Target: target,
		Limits: backupsource.RestoreLimits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("NewReopener: %v", err)
	}

	if _, err := reopener.Reopen(context.Background(), liveRoot, "verified-fingerprint"); !errors.Is(err, reopenFailure) {
		t.Fatalf("Reopen error = %v", err)
	}
	if !reflect.DeepEqual(target.transaction.events, []string{"validate", "commit", "reopen", "rollback"}) {
		t.Fatalf("events = %v", target.transaction.events)
	}
}

func TestReopenerRejectsReceiptForAnotherFingerprintAndRollsBack(t *testing.T) {
	liveRoot := restoredSemanticRoot(t)
	target := &restoreTarget{transaction: &restoreTransaction{receipt: backupsource.RestoreReceipt{ReceiptID: "wrong-state", Fingerprint: "another-fingerprint"}}}
	reopener, err := backupsource.NewReopener(backupsource.ReopenerOptions{
		Target: target,
		Limits: backupsource.RestoreLimits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("NewReopener: %v", err)
	}

	if _, err := reopener.Reopen(context.Background(), liveRoot, "verified-fingerprint"); !errors.Is(err, backupsource.ErrInvalidSource) {
		t.Fatalf("Reopen error = %v, want ErrInvalidSource", err)
	}
	if !reflect.DeepEqual(target.transaction.events, []string{"validate", "commit", "reopen", "rollback"}) {
		t.Fatalf("events = %v", target.transaction.events)
	}
}

func TestDirectoryActivationRollsBackFilesAndTypedStoresTogether(t *testing.T) {
	candidate := restoredSemanticRoot(t)
	root := filepath.Dir(candidate)
	liveRoot := filepath.Join(root, "active")
	if err := os.MkdirAll(liveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveRoot, "old-state"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopenFailure := errors.New("typed stores unavailable")
	target := &restoreTarget{transaction: &restoreTransaction{reopenErr: reopenFailure}}
	reopener, err := backupsource.NewReopener(backupsource.ReopenerOptions{
		Target: target,
		Limits: backupsource.RestoreLimits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("NewReopener: %v", err)
	}
	activator, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{
		LiveDirectory: liveRoot,
		MarkerPath:    filepath.Join(root, "restore.marker"),
		Reopener:      reopener,
	})
	if err != nil {
		t.Fatalf("NewDirectoryActivator: %v", err)
	}
	_, err = activator.Activate(context.Background(), backupflow.ActivationRequest{
		CandidateDir: candidate,
		ComputerID:   "mac",
		Fingerprint:  "verified-fingerprint",
	})
	if !errors.Is(err, reopenFailure) {
		t.Fatalf("Activate error = %v", err)
	}
	oldState, err := os.ReadFile(filepath.Join(liveRoot, "old-state"))
	if err != nil || string(oldState) != "old" {
		t.Fatalf("previous live state was not restored: %q, %v", oldState, err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "settings", "bria.json")); err != nil {
		t.Fatalf("failed candidate was not preserved: %v", err)
	}
	if !reflect.DeepEqual(target.transaction.events, []string{"validate", "commit", "reopen", "rollback"}) {
		t.Fatalf("typed store events = %v", target.transaction.events)
	}
}

func restoredSemanticRoot(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	catalog, err := computer.NewCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Upsert(computer.Record{ID: "mac", Name: "Mac", Fingerprint: "public-fingerprint", Status: computer.StatusOnline, ProtocolVersion: 1, Capabilities: []computer.Capability{{Provider: domain.ProviderCodex, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work/project")
	if err != nil {
		t.Fatal(err)
	}
	source, err := backupsource.New(backupsource.Options{
		Barrier:   &readBarrier{},
		Settings:  settings.NewMemoryStore(),
		Computers: catalog,
		Sessions:  sessionPort{sessions: []domain.Session{session}},
		Journal: journalPort{
			inputs: []messagejournal.Input{{MessageID: "pending", SessionID: "session-1", Sequence: 1, Payload: []byte("input"), Phase: messagejournal.InputPending}},
			outputs: []messagejournal.Output{
				{OperationID: "delivered", SessionID: "session-1", Sequence: 2, Kind: "final", Payload: []byte("already sent"), Phase: messagejournal.OutputConfirmed, Receipt: "excluded-receipt"},
				{OperationID: "failed", SessionID: "session-1", Sequence: 3, Kind: "final", Payload: []byte("output"), Phase: messagejournal.OutputFailed},
			},
		},
		Histories: map[domain.Provider]backupsource.HistorySource{domain.ProviderCodex: history("full codex text"), domain.ProviderClaude: history("unused")},
		Harness:   safeTree{"rules/rules.md": "rules", "settings/settings.json": "{}", "checks/check.go": "package checks", "state/state.json": "{}"},
	})
	if err != nil {
		t.Fatalf("New source: %v", err)
	}
	root := t.TempDir()
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest"),
		ComputerID: "mac", State: source,
		Limits: backupruntime.Limits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := runner.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	liveRoot := filepath.Join(root, "restored")
	if _, err := backup.RestoreCandidate(filepath.Join(root, "latest"), liveRoot); err != nil {
		t.Fatalf("RestoreCandidate: %v", err)
	}
	return liveRoot
}

type restoreTarget struct {
	state       backupsource.RestoredState
	transaction *restoreTransaction
}

func (target *restoreTarget) PrepareRestore(_ context.Context, state backupsource.RestoredState) (backupsource.RestoreTransaction, error) {
	target.state = state
	return target.transaction, nil
}

type restoreTransaction struct {
	events    []string
	receipt   backupsource.RestoreReceipt
	reopenErr error
}

func (transaction *restoreTransaction) Validate(context.Context) error {
	transaction.events = append(transaction.events, "validate")
	return nil
}

func (transaction *restoreTransaction) Commit(context.Context) error {
	transaction.events = append(transaction.events, "commit")
	return nil
}

func (transaction *restoreTransaction) Reopen(context.Context) (backupsource.RestoreReceipt, error) {
	transaction.events = append(transaction.events, "reopen")
	return transaction.receipt, transaction.reopenErr
}

func (transaction *restoreTransaction) Rollback(context.Context) error {
	transaction.events = append(transaction.events, "rollback")
	return nil
}
