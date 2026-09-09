//go:build linux || darwin

package terminalbinding

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNotManagedRequiresTrustedAbsentBinding(t *testing.T) {
	for _, scenario := range []string{"absent-state", "absent-root", "absent-record", "unsafe-root", "unsafe-state", "dangling-root", "dangling-state", "malformed-record"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			state := root
			identity := Identity{LogicalSessionID: "logical", NativeSessionID: "native", Provider: "fake", Workdir: root}
			wantAbsent := false
			switch scenario {
			case "absent-state":
				state, wantAbsent = filepath.Join(root, "missing", "state"), true
			case "absent-root":
				wantAbsent = true
			case "absent-record", "malformed-record":
				lease, err := Acquire(root, identity, true)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "malformed-record" {
					if err := os.WriteFile(lease.path, []byte("not-json"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					wantAbsent = true
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
			case "unsafe-root":
				if err := os.Mkdir(filepath.Join(root, "terminal-bindings"), 0755); err != nil {
					t.Fatal(err)
				}
			case "unsafe-state":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
			case "dangling-root", "dangling-state":
				path := filepath.Join(root, "terminal-bindings")
				if scenario == "dangling-state" {
					state = filepath.Join(root, "state")
					path = state
				}
				if err := os.Symlink(filepath.Join(root, "absent"), path); err != nil {
					t.Fatal(err)
				}
			}
			lease, err := Acquire(state, identity, false)
			if err == nil {
				_, err = lease.Read(identity)
				_ = lease.Close()
			}
			if err == nil || errors.Is(err, ErrNotManaged) != wantAbsent {
				t.Fatalf("absence classification: %v, want NotManaged=%v", err, wantAbsent)
			}
			if wantAbsent && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("compatibility lost: %v", err)
			}
		})
	}
}

func TestPermissionFailureNeverMeansNotManaged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory access permissions")
	}
	root := t.TempDir()
	state := filepath.Join(root, "denied")
	if err := os.Mkdir(state, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(state, 0700) })
	identity := Identity{LogicalSessionID: "logical", NativeSessionID: "native", Provider: "fake", Workdir: root}
	if _, err := Acquire(state, identity, false); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNotManaged) {
		t.Fatalf("permission failure permits fallback: %v", err)
	}
}
