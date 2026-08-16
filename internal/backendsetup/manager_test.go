package backendsetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/systemdeps"
)

type setupRunner struct {
	mu         sync.Mutex
	installed  map[string]bool
	fail       bool
	npmMissing bool
}

func (r *setupRunner) LookPath(name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (name == "npm" && !r.npmMissing) || r.installed[name] {
		return name, nil
	}
	return "", errors.New("missing")
}

func (r *setupRunner) enableNPM() {
	r.mu.Lock()
	r.npmMissing = false
	r.mu.Unlock()
}

func (r *setupRunner) Run(
	_ context.Context, name string, args ...string,
) (runtimehost.CommandResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "npm" {
		if r.fail {
			return runtimehost.CommandResult{ExitCode: 1, Stderr: []byte("registry unavailable")}, nil
		}
		if len(args) < 3 {
			return runtimehost.CommandResult{}, errors.New("missing prefix")
		}
		root := args[2]
		backend := "codex"
		if filepath.Base(root) == "claude" {
			backend = "claude"
		}
		r.installed[filepath.Join(root, "node_modules", ".bin", backend)] = true
		return runtimehost.CommandResult{}, nil
	}
	if r.installed[name] {
		return runtimehost.CommandResult{Stdout: []byte("1.2.3\n")}, nil
	}
	return runtimehost.CommandResult{}, errors.New("missing")
}

func TestManagerInstallsIntoUserOwnedPrefixAndRefreshes(t *testing.T) {
	root := t.TempDir()
	runner := &setupRunner{installed: make(map[string]bool)}
	refreshed := make(chan struct{}, 1)
	manager := newSetupManager(t, root, runner, func(context.Context) { refreshed <- struct{}{} })
	request := Request{NodeID: "node", Backend: "codex"}
	status, err := manager.Start(context.Background(), request)
	if err != nil || status.Phase != PhaseInstalling {
		t.Fatalf("start=%#v, err=%v", status, err)
	}
	waitForSetupPhase(t, manager, request, PhaseReady)
	select {
	case <-refreshed:
	case <-time.After(time.Second):
		t.Fatal("inventory was not refreshed")
	}
}

func TestManagerReportsBoundedInstallFailure(t *testing.T) {
	root := t.TempDir()
	runner := &setupRunner{installed: make(map[string]bool), fail: true}
	manager := newSetupManager(t, root, runner, nil)
	request := Request{NodeID: "node", Backend: "claude"}
	if _, err := manager.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	status := waitForSetupPhase(t, manager, request, PhaseFailed)
	if status.Detail == "" {
		t.Fatal("installation failure omitted its detail")
	}
}

func TestManagerAutomaticallyRequestsNodeJSWhenNPMIsMissing(t *testing.T) {
	root := t.TempDir()
	runner := &setupRunner{installed: make(map[string]bool), npmMissing: true}
	manager := newSetupManager(t, root, runner, nil)
	requests := filepath.Join(root, "system-deps", "requests")
	results := filepath.Join(root, "system-deps", "results")
	if err := os.MkdirAll(results, 0o700); err != nil {
		t.Fatal(err)
	}
	manager.config.Dependencies = systemdeps.Config{RequestDir: requests, ResultDir: results}
	helperDone := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			entries, err := os.ReadDir(requests)
			if err != nil || len(entries) == 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			identity := strings.TrimSuffix(entries[0].Name(), ".request")
			runner.enableNPM()
			helperDone <- os.WriteFile(
				filepath.Join(results, identity+".result"), []byte("ready\n"), 0o600,
			)
			return
		}
		helperDone <- context.DeadlineExceeded
	}()
	request := Request{NodeID: "node", Backend: "codex"}
	if _, err := manager.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := <-helperDone; err != nil {
		t.Fatal(err)
	}
	waitForSetupPhase(t, manager, request, PhaseReady)
}

func newSetupManager(
	t *testing.T, root string, runner runtimehost.CommandRunner, refresh func(context.Context),
) *Manager {
	t.Helper()
	roots := map[string]string{
		"claude": filepath.Join(root, "claude"), "codex": filepath.Join(root, "codex"),
	}
	manager, err := NewManager(Config{
		NodeID: "node", Roots: roots,
		Commands: map[string]string{
			"claude": filepath.Join(roots["claude"], "node_modules", ".bin", "claude"),
			"codex":  filepath.Join(roots["codex"], "node_modules", ".bin", "codex"),
		},
		Runner: runner, Refresh: refresh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func waitForSetupPhase(
	t *testing.T, manager *Manager, request Request, want Phase,
) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backend setup did not reach %s", want)
	return Status{}
}
