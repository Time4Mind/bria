//go:build linux || darwin

package terminalbinding

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateManifestLeaseBoundsAndNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	identity, err := Canonical(Identity{LogicalSessionID: "logical", NativeSessionID: "native", Provider: "data-only", Workdir: root})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(root, identity, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := Acquire(root, identity, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing manifest: %v", err)
	}
	record := Record{Version: 1, Identity: identity, ServerPID: 2, PanePID: 3, ServerBirth: "s", PaneBirth: "p", PaneID: "%0", Nonce: strings.Repeat("a", 64)}
	if err := lease.Save(record); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(root, identity, false); !errors.Is(err, ErrOwned) {
		t.Fatalf("duplicate owner: %v", err)
	}
	if err := lease.Save(record); !errors.Is(err, ErrMismatch) {
		t.Fatalf("existing record overwritten: %v", err)
	}
	if got, err := lease.Read(identity); err != nil || got != record {
		t.Fatalf("manifest roundtrip: %+v %v", got, err)
	}
	info, err := os.Stat(lease.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("manifest permissions: %v %v", info, err)
	}
	if err := os.Chmod(lease.path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Read(identity); !errors.Is(err, ErrMismatch) {
		t.Fatalf("public metadata accepted: %v", err)
	}
	if err := os.Chmod(lease.path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lease.path, []byte(strings.Repeat("x", 8193)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Read(identity); !errors.Is(err, ErrMismatch) {
		t.Fatalf("unbounded manifest accepted: %v", err)
	}
}

func TestManifestRejectsSymlinkAndUnsafeIdentity(t *testing.T) {
	root := t.TempDir()
	identity := Identity{LogicalSessionID: "logical", NativeSessionID: "native", Provider: "fake", Workdir: root}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "terminal-bindings")); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(root, identity, true); !errors.Is(err, ErrMismatch) {
		t.Fatalf("symlink root accepted: %v", err)
	}
	for _, value := range []string{"", " leading", "bad\nvalue", strings.Repeat("x", 257)} {
		identity.Provider = value
		if _, err := Canonical(identity); !errors.Is(err, ErrMismatch) {
			t.Fatalf("unsafe provider identity: %v", err)
		}
	}
}

func TestProcessBirthIsStableExactAndCancellationAware(t *testing.T) {
	first, err := ProcessBirth(context.Background(), os.Getpid())
	if err != nil || first == "" {
		t.Fatalf("birth proof unavailable: %v", err)
	}
	second, err := ProcessBirth(context.Background(), os.Getpid())
	if err != nil || first != second {
		t.Fatalf("process birth changed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProcessBirth(ctx, os.Getpid()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if _, err := ProcessBirth(context.Background(), 0); !errors.Is(err, ErrMismatch) {
		t.Fatalf("unsafe PID accepted: %v", err)
	}
}
