package backupcomposition_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bria/internal/backupcomposition"
	"bria/internal/backupflow"
	"bria/internal/backupruntime"
	"bria/internal/backupsource"
	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/messagejournal"
	"bria/internal/settings"
	"bria/internal/storage"
)

func TestManualRuntimeCreatesCorruptsAndRestoresOnlyCanonicalState(t *testing.T) {
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	settingsStore, err := settings.OpenFileStore(filepath.Join(stateDirectory, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := computer.OpenCatalogFile(filepath.Join(root, "nodes", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Upsert(computer.Record{ID: "mac", Name: "Mac", Fingerprint: "public", Status: computer.StatusOnline, ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}
	session, err := domain.NewStartingSession("session-1", "intent-1", "mac", domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := storage.OpenSessionStore(filepath.Join(stateDirectory, "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := sessions.PutStartingIfAbsent(context.Background(), session); err != nil || !inserted {
		t.Fatalf("PutStartingIfAbsent inserted=%t err=%v", inserted, err)
	}
	journal, err := messagejournal.Open(filepath.Join(stateDirectory, "journal.json"), messagejournal.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := backupcomposition.New(backupcomposition.Options{
		ComputerID: "mac", WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "backup", "latest"),
		RestoreCandidateDir: filepath.Join(root, "restore", "candidate"), LiveDirectory: filepath.Join(root, "live"), MarkerPath: filepath.Join(root, "restore.commit"),
		Policy: backupcomposition.Policy{Schedule: backupcomposition.AutomaticScheduleDisabled, Encryption: backupcomposition.EncryptionDisabled},
		Sources: backupcomposition.Sources{
			Settings: settingsStore, Computers: catalog, Sessions: sessions, Journal: journal,
			HistoryRoots: map[domain.Provider]string{domain.ProviderCodex: filepath.Join(root, "history", "codex"), domain.ProviderClaude: filepath.Join(root, "history", "claude")},
			HarnessRoot:  filepath.Join(root, "harness"),
		},
		Limits: backupcomposition.Limits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mustWrite(t, filepath.Join(root, "history", "codex", "session-1.jsonl"), "full text")
	for _, path := range []string{"rules/rules.md", "settings/settings.json", "checks/check.txt", "state/state.json"} {
		mustWrite(t, filepath.Join(root, "harness", path), "safe")
	}
	mustWrite(t, filepath.Join(root, "harness", "state", "photos", "not-eligible"), "not selected")
	if _, err := runtime.TriggerBackup(context.Background()); err != nil {
		t.Fatalf("TriggerBackup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "history", "codex", "session-1.jsonl"), []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := runtime.PrepareRestore()
	if err != nil {
		t.Fatalf("PrepareRestore: %v", err)
	}
	receipt, err := runtime.ActivateRestore(context.Background(), prepared)
	if err != nil {
		t.Fatalf("ActivateRestore: %v", err)
	}
	if !strings.HasPrefix(receipt.ReceiptID, "canonical-root:") || receipt.Fingerprint != prepared.Fingerprint {
		t.Fatalf("canonical root receipt = %#v", receipt)
	}
	got, err := os.ReadFile(filepath.Join(root, "live", "history", "text", "codex", "session-1.jsonl"))
	if err != nil || string(got) != "full text" {
		t.Fatalf("restored history = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "live", "harness", "state", "photos")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("forbidden photo path restored: %v", err)
	}

	// Inject a post-rename corruption. The target must reread the switched
	// canonical root, reject it, and let DirectoryActivator restore the prior
	// complete root rather than leaving a partial restore visible.
	if _, err := runtime.TriggerBackup(context.Background()); err != nil {
		t.Fatalf("TriggerBackup before failure injection: %v", err)
	}
	prepared, err = runtime.PrepareRestore()
	if err != nil {
		t.Fatalf("PrepareRestore before failure injection: %v", err)
	}
	target, err := backupcomposition.NewCanonicalRootTarget(filepath.Join(root, "live"), backupsource.RestoreLimits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20}, runtime.ReadBarrier())
	if err != nil {
		t.Fatalf("NewCanonicalRootTarget: %v", err)
	}
	reopener, err := backupsource.NewReopener(backupsource.ReopenerOptions{Target: target, Limits: backupsource.RestoreLimits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20}})
	if err != nil {
		t.Fatalf("NewReopener: %v", err)
	}
	activator, err := backupruntime.NewDirectoryActivator(backupruntime.ActivatorOptions{
		LiveDirectory: filepath.Join(root, "live"), MarkerPath: filepath.Join(root, "restore.failure.commit"), Reopener: reopener,
		AfterStage: func(stage string) error {
			if stage == "swapped" {
				if err := os.WriteFile(filepath.Join(root, "live", "settings", "bria.json"), []byte("corrupt"), 0o600); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewDirectoryActivator: %v", err)
	}
	flow := backupflow.Service{SourceRoot: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "backup", "latest"), RestoreCandidateDir: filepath.Join(root, "restore", "candidate"), ComputerID: "mac", Layout: backupflow.CanonicalSnapshotLayout()}
	if _, err := flow.ActivateRestore(context.Background(), prepared, activator); err == nil {
		t.Fatal("post-swap corruption activated successfully")
	}
	got, err = os.ReadFile(filepath.Join(root, "live", "history", "text", "codex", "session-1.jsonl"))
	if err != nil || string(got) != "full text" {
		t.Fatalf("previous canonical root was not restored after injected failure: %q, %v", got, err)
	}
}

func TestPolicyRequiresExplicitEncryptionAndRejectsOwnerStorageUse(t *testing.T) {
	if _, err := backupcomposition.New(backupcomposition.Options{}); !errors.Is(err, backupcomposition.ErrPolicyDecisionRequired) {
		t.Fatalf("unspecified policy error = %v", err)
	}
	if _, err := backupcomposition.New(backupcomposition.Options{Policy: backupcomposition.Policy{
		Schedule: backupcomposition.AutomaticScheduleDisabled, Encryption: backupcomposition.EncryptionDisabled, OwnerStore: unusedOwnerStore{},
	}}); !errors.Is(err, backupcomposition.ErrPolicyDecisionRequired) {
		t.Fatalf("owner storage policy error = %v", err)
	}
}

func TestReadBarrierDoesNotOverlapSnapshotReadAndRestoreWrite(t *testing.T) {
	barrier := backupcomposition.NewReadBarrier()
	releaseRead, err := barrier.BeginRead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func() error, 1)
	go func() {
		release, err := barrier.BeginWrite(context.Background())
		if err != nil {
			t.Errorf("BeginWrite: %v", err)
			return
		}
		acquired <- release
	}()
	select {
	case <-acquired:
		t.Fatal("restore write overlapped snapshot read")
	default:
	}
	if err := releaseRead(); err != nil {
		t.Fatal(err)
	}
	select {
	case release := <-acquired:
		if err := release(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("restore write did not proceed after snapshot read")
	}
}

type unusedOwnerStore struct{}

func (unusedOwnerStore) ReplaceLatest(context.Context, string, string) (backupruntime.ExternalReceipt, error) {
	return backupruntime.ExternalReceipt{}, nil
}

func mustWrite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
