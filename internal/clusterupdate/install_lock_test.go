package clusterupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallLockPreservesLiveOwnerAndRecoversStaleOwner(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	first, err := acquireInstallLock(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstallLock(root, now); err == nil {
		t.Fatal("concurrent install lock unexpectedly succeeded")
	}
	if err := os.WriteFile(
		filepath.Join(root, ".install.lock", "owner"),
		[]byte(first.owner+"|process-start-token\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-staleInstallLockAge - time.Second)
	if err := os.Chtimes(filepath.Join(root, ".install.lock"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstallLock(root, now); err == nil {
		t.Fatal("aged live install lock was stolen")
	}
	if _, err := os.Stat(filepath.Join(root, ".install.lock", "owner")); err != nil {
		t.Fatalf("losing contender removed live lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".install.lock", "owner"), []byte(first.owner+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(root, ".install.lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := old
	if err := os.Chtimes(lockPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	recovered, err := acquireInstallLock(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}
