package backupruntime_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bria/internal/backup"
	"bria/internal/backupflow"
	"bria/internal/backupruntime"
)

func TestRunOnceExportsOneTransactionalSemanticSnapshot(t *testing.T) {
	root := t.TempDir()
	transaction := semanticTransaction()
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest.bria-backup"),
		ComputerID: "macbook", State: &statePort{transaction: transaction}, Limits: runtimeLimits(),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	result, err := runner.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if transaction.closed != 1 {
		t.Fatalf("transaction close count = %d", transaction.closed)
	}
	manifest, err := backup.Validate(result.LatestPath)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(manifest.Files) != 9 {
		t.Fatalf("files = %d, want 9: %#v", len(manifest.Files), manifest.Files)
	}
	entries, err := os.ReadDir(filepath.Join(root, "work"))
	if err != nil {
		t.Fatalf("read work: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("transactional staging leaked: %#v", entries)
	}
}

func TestRunOnceFailsClosedForMissingPortsAndExternalPolicies(t *testing.T) {
	root := t.TempDir()
	base := backupruntime.RunOptions{WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest"), ComputerID: "mac", State: &statePort{transaction: semanticTransaction()}, Limits: runtimeLimits()}
	missing := base
	missing.State = &statePort{transaction: &snapshotTransaction{sources: backupruntime.Sources{}}}
	if runner, err := backupruntime.NewRunner(missing); err == nil {
		if _, err := runner.RunOnce(context.Background()); !errors.Is(err, backupruntime.ErrInvalidRuntime) {
			t.Fatalf("missing source error = %v", err)
		}
	}
	remote := base
	remote.RemoteRequired = true
	if _, err := backupruntime.NewRunner(remote); !errors.Is(err, backupruntime.ErrInvalidRuntime) {
		t.Fatalf("missing remote error = %v", err)
	}
	encryption := base
	encryption.EncryptionRequired = true
	if _, err := backupruntime.NewRunner(encryption); !errors.Is(err, backupruntime.ErrInvalidRuntime) {
		t.Fatalf("missing protector error = %v", err)
	}
}

func TestRunOnceBoundsSnapshotBeforeItCanFillStaging(t *testing.T) {
	root := t.TempDir()
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest"),
		ComputerID: "mac", State: &statePort{transaction: semanticTransaction()},
		Limits: backupruntime.Limits{MaxFiles: 4, MaxFileBytes: 8, MaxTotalBytes: 16},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); !errors.Is(err, backupruntime.ErrInvalidRuntime) {
		t.Fatalf("bounded RunOnce error = %v, want ErrInvalidRuntime", err)
	}
	if _, err := os.Stat(filepath.Join(root, "latest")); !os.IsNotExist(err) {
		t.Fatalf("oversized snapshot promoted: %v", err)
	}
}

func TestSnapshotTransactionMustCloseCleanlyBeforeLatestPromotion(t *testing.T) {
	root := t.TempDir()
	transaction := semanticTransaction()
	transaction.closeErr = errors.New("snapshot consistency failed")
	runner, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work"), LatestPath: filepath.Join(root, "latest"),
		ComputerID: "mac", State: &statePort{transaction: transaction}, Limits: runtimeLimits(),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce error = nil")
	}
	if _, err := os.Stat(filepath.Join(root, "latest")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted snapshot promoted: %v", err)
	}
}

func TestEncryptionHappensInStagingBeforeLatestPromotion(t *testing.T) {
	root := t.TempDir()
	latest := filepath.Join(root, "latest")
	if err := os.WriteFile(latest, []byte("previous-protected"), 0o600); err != nil {
		t.Fatalf("write previous: %v", err)
	}
	failing, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work-fail"), LatestPath: latest, ComputerID: "mac",
		State: &statePort{transaction: semanticTransaction()}, Limits: runtimeLimits(),
		EncryptionRequired: true, Protector: protector{err: errors.New("protect failed")},
	})
	if err != nil {
		t.Fatalf("NewRunner(failing): %v", err)
	}
	if _, err := failing.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce(protector failure) error = nil")
	}
	assertRuntimeContent(t, latest, "previous-protected")

	success, err := backupruntime.NewRunner(backupruntime.RunOptions{
		WorkDirectory: filepath.Join(root, "work-ok"), LatestPath: latest, ComputerID: "mac",
		State: &statePort{transaction: semanticTransaction()}, Limits: runtimeLimits(),
		EncryptionRequired: true, Protector: protector{},
	})
	if err != nil {
		t.Fatalf("NewRunner(success): %v", err)
	}
	result, err := success.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce(success): %v", err)
	}
	assertRuntimeContent(t, latest, "protected-artifact")
	if !result.Encrypted || result.ProtectionReceiptID != "protect-1" || result.LatestPath != latest || result.BackupFingerprint == result.ArtifactFingerprint {
		t.Fatalf("encrypted result = %#v", result)
	}
	if _, err := backup.Validate(latest); err == nil {
		t.Fatal("protected latest unexpectedly contains plaintext backup")
	}
}

func TestSchedulerHasNoImplicitScheduleAndRunsOnlyExplicitInterval(t *testing.T) {
	manualRunner := &countingRunner{}
	manual, err := backupruntime.NewScheduler(manualRunner, 0)
	if err != nil {
		t.Fatalf("NewScheduler(manual): %v", err)
	}
	if err := manual.Run(context.Background()); !errors.Is(err, backupruntime.ErrManualOnly) {
		t.Fatalf("manual Run error = %v", err)
	}
	if _, err := manual.RunOnce(context.Background()); err != nil || manualRunner.count() != 1 {
		t.Fatalf("manual RunOnce count=%d err=%v", manualRunner.count(), err)
	}

	periodicRunner := &countingRunner{called: make(chan struct{}, 2)}
	periodic, err := backupruntime.NewScheduler(periodicRunner, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("NewScheduler(periodic): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- periodic.Run(ctx) }()
	select {
	case <-periodicRunner.called:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("explicit periodic schedule did not run")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("periodic Run error = %v", err)
	}
}

type document string

func (d document) Export(_ context.Context, writer io.Writer) error {
	_, err := io.Copy(writer, bytes.NewBufferString(string(d)))
	return err
}

type tree map[string]string

func (source tree) Export(_ context.Context, sink backupruntime.TreeSink) error {
	for path, content := range source {
		if err := sink.WriteFile(path, bytes.NewBufferString(content)); err != nil {
			return err
		}
	}
	return nil
}

type snapshotTransaction struct {
	sources  backupruntime.Sources
	closed   int
	closeErr error
}

func (s *snapshotTransaction) Sources() backupruntime.Sources { return s.sources }
func (s *snapshotTransaction) Close() error                   { s.closed++; return s.closeErr }
func semanticTransaction() *snapshotTransaction {
	return &snapshotTransaction{sources: backupruntime.Sources{
		Settings: document(`{"version":1}`), Computers: document(`{"computers":[]}`),
		Sessions: document(`{"sessions":[]}`), UndeliveredMessages: document(`{"messages":[]}`),
		TextHistory: tree{"session.jsonl": "history"},
		Harness:     tree{"rules/rules.md": "rules", "settings/settings.json": "{}", "checks/check.go": "package checks", "state/state.json": "{}"},
	}}
}

type statePort struct{ transaction *snapshotTransaction }

func (s *statePort) BeginSnapshot(context.Context) (backupruntime.SnapshotTransaction, error) {
	return s.transaction, nil
}

type countingRunner struct {
	mu     sync.Mutex
	calls  int
	called chan struct{}
}

func (r *countingRunner) RunOnce(context.Context) (backupruntime.Result, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if r.called != nil {
		select {
		case r.called <- struct{}{}:
		default:
		}
	}
	return backupruntime.Result{}, nil
}
func (r *countingRunner) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

func runtimeLimits() backupruntime.Limits {
	return backupruntime.Limits{MaxFiles: 32, MaxFileBytes: 1 << 20, MaxTotalBytes: 8 << 20}
}

type protector struct{ err error }

func (p protector) Protect(_ context.Context, source backupflow.VerifiedCopy, output string) (backupruntime.ProtectedCopy, error) {
	if p.err != nil {
		return backupruntime.ProtectedCopy{}, p.err
	}
	artifact := []byte("protected-artifact")
	if err := os.WriteFile(output, artifact, 0o600); err != nil {
		return backupruntime.ProtectedCopy{}, err
	}
	digest := sha256.Sum256(artifact)
	return backupruntime.ProtectedCopy{
		Path: output, SourceFingerprint: source.Fingerprint,
		Fingerprint: hex.EncodeToString(digest[:]), ReceiptID: "protect-1",
	}, nil
}
