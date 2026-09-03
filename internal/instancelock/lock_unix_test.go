//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package instancelock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExclusiveLockIsHeldUntilCloseAndLockFileRemains(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(t.TempDir(), "state.json")
	lock, err := Acquire(statePath)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	if _, err := Acquire(statePath); !errors.Is(err, ErrAlreadyLocked) {
		t.Fatalf("second Acquire() error = %v, want ErrAlreadyLocked", err)
	}
	if code := runLockHelper(t, statePath); code != helperContendedExitCode {
		t.Fatalf("contending subprocess exit = %d, want %d", code, helperContendedExitCode)
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("idempotent Close() error = %v", err)
	}
	if code := runLockHelper(t, statePath); code != 0 {
		t.Fatalf("subprocess after Close exit = %d, want 0", code)
	}

	info, err := os.Lstat(lockFilePath(statePath))
	if err != nil {
		t.Fatalf("Lstat(lock file) error = %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %v, want regular 0600", info.Mode())
	}
}

func TestCanonicalParentAliasesContendForSameLock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatalf("Mkdir(real parent) error = %v", err)
	}
	aliasParent := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatalf("Symlink(parent) error = %v", err)
	}

	lock, err := Acquire(filepath.Join(realParent, "state.json"))
	if err != nil {
		t.Fatalf("Acquire(real path) error = %v", err)
	}
	defer lock.Close()
	if _, err := Acquire(filepath.Join(aliasParent, "state.json")); !errors.Is(err, ErrAlreadyLocked) {
		t.Fatalf("Acquire(alias path) error = %v, want ErrAlreadyLocked", err)
	}
}

func TestRejectsSymlinkLockFileWithoutChangingTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	statePath := filepath.Join(root, "state.json")
	target := filepath.Join(root, "target")
	want := []byte("do not change")
	if err := os.WriteFile(target, want, 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(target, lockFilePath(statePath)); err != nil {
		t.Fatalf("Symlink(lock file) error = %v", err)
	}

	if _, err := Acquire(statePath); !errors.Is(err, ErrLockUnavailable) {
		t.Fatalf("Acquire() error = %v, want ErrLockUnavailable", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("symlink target = %q, want unchanged %q", got, want)
	}
}

func TestErrorsDoNotExposeStatePath(t *testing.T) {
	t.Parallel()

	secretPart := "private-secret-state-name"
	statePath := filepath.Join(t.TempDir(), "missing-parent", secretPart)
	_, err := Acquire(statePath)
	if err == nil {
		t.Fatal("Acquire() error = nil, want failure")
	}
	if strings.Contains(err.Error(), secretPart) || strings.Contains(err.Error(), statePath) {
		t.Fatalf("Acquire() error exposes state path: %q", err)
	}
}

const helperContendedExitCode = 23

func TestInstanceLockSubprocessHelper(t *testing.T) {
	if os.Getenv("BRIA_INSTANCELOCK_HELPER") != "1" {
		return
	}
	lock, err := Acquire(os.Getenv("BRIA_INSTANCELOCK_STATE_PATH"))
	if errors.Is(err, ErrAlreadyLocked) {
		os.Exit(helperContendedExitCode)
	}
	if err != nil {
		os.Exit(24)
	}
	if err := lock.Close(); err != nil {
		os.Exit(25)
	}
	os.Exit(0)
}

func runLockHelper(t *testing.T, statePath string) int {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestInstanceLockSubprocessHelper$")
	command.Env = append(os.Environ(),
		"BRIA_INSTANCELOCK_HELPER=1",
		"BRIA_INSTANCELOCK_STATE_PATH="+statePath,
	)
	err := command.Run()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run helper: %v", err)
	}
	return exitError.ExitCode()
}
