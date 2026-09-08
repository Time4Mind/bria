package orphanresume

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestCleanupSelectionPreservesExactIdentityRules(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		want          bool
	}{
		{"codex", "/bin/codex\x00resume\x00session-1\x00", true},
		{"claude", "/bin/claude\x00resume\x00session-1\x00", true},
		{"codex-cli", "codex-cli\x00resume\x00session-1\x00", true},
		{"claude-code", "claude-code\x00resume\x00session-1\x00", true},
		{"provider-later-in-argv", "node\x00/bin/codex\x00resume\x00session-1\x00", true},
		{"id-prefix", "codex\x00resume\x00session-10\x00", false},
		{"id-not-adjacent", "codex\x00resume\x00--flag\x00session-1\x00", false},
		{"resume-missing", "codex\x00session-1\x00", false},
		{"resume-without-id", "codex\x00resume\x00", false},
		{"unknown-provider", "other\x00resume\x00session-1\x00", false},
		{"provider-prefix", "codex-other\x00resume\x00session-1\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, workdir := t.TempDir(), t.TempDir()
			procEntry(t, root, 90000001, tc.command, workdir)
			var terminated []int
			if err := cleanup("session-1", workdir, root, func(pid int) error {
				terminated = append(terminated, pid)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var want []int
			if tc.want {
				want = []int{90000001}
			}
			if !reflect.DeepEqual(terminated, want) {
				t.Fatalf("terminated = %v, want %v", terminated, want)
			}
		})
	}
}

func TestCleanupCanonicalWorkdirAndExcludedProcesses(t *testing.T) {
	root, workdir := t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(workdir, alias); err != nil {
		t.Fatal(err)
	}
	command := "codex\x00resume\x00session-1\x00"
	procEntry(t, root, 90000001, command, alias)
	procEntry(t, root, 90000002, command, t.TempDir())
	procEntry(t, root, 90000003, command, filepath.Join(workdir, "missing"))
	procEntry(t, root, 0, command, workdir)
	procEntry(t, root, 1, command, workdir)
	procEntry(t, root, os.Getpid(), command, workdir)
	if err := os.Mkdir(filepath.Join(root, "not-a-pid"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "90000004"), 0700); err != nil {
		t.Fatal(err)
	}
	procEntry(t, root, 90000005, command, workdir)
	if err := os.Remove(filepath.Join(root, "90000005", "cwd")); err != nil {
		t.Fatal(err)
	}
	var terminated []int
	if err := cleanup("session-1", alias, root, func(pid int) error {
		terminated = append(terminated, pid)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(terminated, []int{90000001}) {
		t.Fatalf("terminated = %v, want [90000001]", terminated)
	}
}

func TestCleanupNoopConditionsNeverTerminate(t *testing.T) {
	root, workdir := t.TempDir(), t.TempDir()
	procEntry(t, root, 90000001, "codex\x00resume\x00session-1\x00", workdir)
	for _, tc := range []struct{ id, workdir, root string }{
		{"", workdir, root}, {" \t ", workdir, root},
		{"session-1", filepath.Join(workdir, "missing"), root},
		{"session-1", workdir, filepath.Join(root, "missing")},
	} {
		if err := cleanup(tc.id, tc.workdir, tc.root, func(pid int) error {
			t.Fatalf("unexpected termination of %d", pid)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCleanupStopsAtFirstTerminationErrorWithPIDContext(t *testing.T) {
	root, workdir := t.TempDir(), t.TempDir()
	for _, pid := range []int{90000001, 90000002} {
		procEntry(t, root, pid, "claude\x00resume\x00session-1\x00", workdir)
	}
	denied := errors.New("termination denied")
	var terminated []int
	err := cleanup("session-1", workdir, root, func(pid int) error {
		terminated = append(terminated, pid)
		return denied
	})
	if !errors.Is(err, denied) || err.Error() != "pid 90000001: termination denied" {
		t.Fatalf("cleanup error = %v", err)
	}
	if !reflect.DeepEqual(terminated, []int{90000001}) {
		t.Fatalf("terminated = %v, want [90000001]", terminated)
	}
}

// A synthetic proc table and a receipt replace process signalling entirely.
func TestCleanupExactResumeWritesTerminationReceipt(t *testing.T) {
	root, workdir := t.TempDir(), t.TempDir()
	procEntry(t, root, 90000001, "codex\x00resume\x00session-1\x00", workdir)
	receipt := filepath.Join(t.TempDir(), "terminated")
	err := cleanup(" session-1 ", workdir, root, func(pid int) error {
		return os.WriteFile(receipt, []byte(strconv.Itoa(pid)), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(receipt)
	if err != nil || string(got) != "90000001" {
		t.Fatalf("termination receipt = %q, %v; want 90000001", got, err)
	}
}

func procEntry(t *testing.T, root string, pid int, command, workdir string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(command), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workdir, filepath.Join(dir, "cwd")); err != nil {
		t.Fatal(err)
	}
}
