package updateinstall_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/updateinstall"
)

func TestFileInstallLockAtPathUsesExactPhysicalPathAndReopens(t *testing.T) {
	t.Parallel()
	root := privateLockDirectory(t)
	path := filepath.Join(root, "runtime-update.lock")
	lock, err := updateinstall.OpenFileInstallLockAtPath(path)
	if err != nil {
		t.Fatalf("OpenFileInstallLockAtPath: %v", err)
	}
	if err := lock.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat exact lock path: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock permissions = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, ".update-install.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("derived install-root lock exists or stat failed: %v", err)
	}
	reopened, err := updateinstall.OpenFileInstallLockAtPath(path)
	if err != nil {
		t.Fatalf("reopen exact lock path: %v", err)
	}
	if err := reopened.WithLock(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("WithLock after reopen: %v", err)
	}
}

func TestFileInstallLockAtPathSerializesReopenedInstances(t *testing.T) {
	t.Parallel()
	path := filepath.Join(privateLockDirectory(t), "runtime-update.lock")
	first, err := updateinstall.OpenFileInstallLockAtPath(path)
	if err != nil {
		t.Fatalf("first OpenFileInstallLockAtPath: %v", err)
	}
	second, err := updateinstall.OpenFileInstallLockAtPath(path)
	if err != nil {
		t.Fatalf("second OpenFileInstallLockAtPath: %v", err)
	}
	entered := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 2)
	go func() {
		done <- first.WithLock(context.Background(), func() error {
			entered <- "first"
			<-release
			return nil
		})
	}()
	if got := <-entered; got != "first" {
		t.Fatalf("first entry = %q", got)
	}
	go func() {
		done <- second.WithLock(context.Background(), func() error {
			entered <- "second"
			return nil
		})
	}()
	select {
	case got := <-entered:
		t.Fatalf("overlapping entry = %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := <-entered; got != "second" {
		t.Fatalf("second entry = %q", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFileInstallLockAtPathRejectsUnsafePath(t *testing.T) {
	t.Parallel()
	root := privateLockDirectory(t)
	for _, path := range []string{
		"runtime-update.lock",
		root + string(filepath.Separator) + "nested" + string(filepath.Separator) + ".." + string(filepath.Separator) + "runtime-update.lock",
		filepath.Join(root, "missing", "runtime-update.lock"),
	} {
		if lock, err := updateinstall.OpenFileInstallLockAtPath(path); !errors.Is(err, updateinstall.ErrInvalidInstaller) || lock != nil {
			t.Errorf("OpenFileInstallLockAtPath(%q) = %#v, %v; want nil ErrInvalidInstaller", path, lock, err)
		}
	}
}

func privateLockDirectory(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
