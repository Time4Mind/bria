//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMacOSAtomicSymlinkReplacesDirectoryTargetWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, directory := range []string{first, second} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(root, "current")
	if err := os.Symlink(first, current); err != nil {
		t.Fatal(err)
	}
	helper, err := filepath.Abs(filepath.Join("..", "..", "scripts", "macos-release-layout.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		"/bin/sh", "-c", `. "$1"; macos_atomic_symlink "$2" "$3" "$4"`,
		"test", helper, second, current, filepath.Join(root, ".current.new"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("replace activation symlink: %v: %s", err, output)
	}
	target, err := os.Readlink(current)
	if err != nil {
		t.Fatal(err)
	}
	if target != second {
		t.Fatalf("current target = %q, want %q", target, second)
	}
	entries, err := os.ReadDir(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary symlink was moved into old release: %v", entries)
	}
}

func TestMacOSInstallLockDoesNotRemoveLiveOwnerAndRecoversStaleOwner(t *testing.T) {
	helper, err := filepath.Abs(filepath.Join("..", "..", "scripts", "macos-release-layout.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(t.TempDir(), ".install.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lock, "owner"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", `. "$1"; ! macos_acquire_install_lock "$2"`, "test", helper, lock)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("live owner lock: %v: %s", err, output)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("losing installer removed live lock: %v", err)
	}
	stale := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("/bin/sh", "-c", `. "$1"; ! macos_acquire_install_lock "$2"`, "test", helper, lock)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("aged live owner lock: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(lock, "owner"), []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = exec.Command(
		"/bin/sh", "-c", `. "$1"; macos_acquire_install_lock "$2"; macos_release_install_lock "$2"`,
		"test", helper, lock,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("stale owner lock: %v: %s", err, output)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("recovered lock remains: %v", err)
	}
}

func TestMacOSLauncherUsesExactCustomActivationBinary(t *testing.T) {
	launcher, err := filepath.Abs(filepath.Join("..", "..", "scripts", "launch-current-macos.sh"))
	if err != nil {
		t.Fatal(err)
	}
	activation := filepath.Join(t.TempDir(), "bria-live")
	if err := os.Mkdir(activation, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(activation, "bria")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$BRIA_EXPECTED_BINARY_SHA256\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	want, err := binarySHA256(binary)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("/bin/sh", launcher, binary).CombinedOutput()
	if err != nil {
		t.Fatalf("launch custom activation: %v: %s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("launcher identity = %q, want %q", got, want)
	}
}
