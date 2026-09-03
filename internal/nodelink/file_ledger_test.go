package nodelink_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/nodelink"
)

func TestFileOperationLedgerDeduplicatesAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	ledger, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	op := nodelink.Operation{ID: "operation-1", Digest: "digest-1"}
	applied := 0
	duplicate, err := ledger.ApplyOnce(context.Background(), op, func() error { applied++; return nil })
	if err != nil || duplicate {
		t.Fatalf("first ApplyOnce = duplicate %v, error %v", duplicate, err)
	}

	reopened, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err = reopened.ApplyOnce(context.Background(), op, func() error { applied++; return nil })
	if err != nil || !duplicate {
		t.Fatalf("reopened ApplyOnce = duplicate %v, error %v", duplicate, err)
	}
	if applied != 1 {
		t.Fatalf("apply count = %d, want 1", applied)
	}
	_, err = reopened.ApplyOnce(context.Background(), nodelink.Operation{ID: op.ID, Digest: "different"}, func() error { return nil })
	if !errors.Is(err, nodelink.ErrOperationConflict) {
		t.Fatalf("conflicting ApplyOnce error = %v", err)
	}
}

func TestFileOperationLedgerReconcilesInterruptedCommitBeforeRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"entries":[{"id":"operation-1","digest":"digest-1","phase":"pending"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	op := nodelink.Operation{ID: "operation-1", Digest: "digest-1"}
	if _, err := ledger.ApplyOnce(context.Background(), op, func() error { return nil }); !errors.Is(err, nodelink.ErrOperationInDoubt) {
		t.Fatalf("pending ApplyOnce error = %v", err)
	}
	if got := ledger.InDoubtOperations(); len(got) != 1 || got[0] != op {
		t.Fatalf("InDoubtOperations = %#v, want %#v", got, []nodelink.Operation{op})
	}
	if err := ledger.Resolve(context.Background(), op, nodelink.OperationNotApplied); err != nil {
		t.Fatal(err)
	}
	applied := 0
	if duplicate, err := ledger.ApplyOnce(context.Background(), op, func() error { applied++; return nil }); err != nil || duplicate {
		t.Fatalf("reconciled ApplyOnce = duplicate %v, error %v", duplicate, err)
	}
	if applied != 1 {
		t.Fatalf("apply count = %d, want 1", applied)
	}
}

func TestFileOperationLedgerFailedResolutionRemainsInDoubtInMemory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "operations.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"entries":[{"id":"operation-1","digest":"digest-1","phase":"pending"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger, err := nodelink.OpenFileOperationLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("storage unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := nodelink.Operation{ID: "operation-1", Digest: "digest-1"}
	if err := ledger.Resolve(context.Background(), operation, nodelink.OperationApplied); err == nil {
		t.Fatal("resolution unexpectedly persisted")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ApplyOnce(context.Background(), operation, func() error { return nil }); !errors.Is(err, nodelink.ErrOperationInDoubt) {
		t.Fatalf("live ledger after failed resolution error=%v", err)
	}
}
