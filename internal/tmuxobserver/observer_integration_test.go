//go:build darwin || linux

package tmuxobserver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealTmuxObserverPreservesGeometryAndReportsOutputAndPaneExit(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	root, err := os.MkdirTemp("/tmp", "bria-tmuxobserver-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "tmux.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	environment := withoutEnvironment(os.Environ(), "TMUX")
	environment = append(environment, "TMUX=")
	runTmux(t, ctx, environment, tmux, "-u", "-f", os.DevNull, "-S", socket,
		"new-session", "-d", "-s", "cli", "-x", "120", "-y", "40", "--", "/bin/cat")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		command := exec.CommandContext(cleanup, tmux, "-u", "-N", "-S", socket, "kill-server")
		command.Env = environment
		_ = command.Run()
	})

	before := strings.TrimSpace(runTmux(t, ctx, environment, tmux, "-u", "-N", "-S", socket,
		"display-message", "-p", "-t", "cli:0.0", "#{window_width}x#{window_height}"))
	observer, err := Start(ctx, Spec{Executable: tmux, Socket: socket, Target: "cli:0.0", Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	select {
	case <-observer.Updates():
	case <-ctx.Done():
		t.Fatal("initial observer hint missing")
	}
	after := strings.TrimSpace(runTmux(t, ctx, environment, tmux, "-u", "-N", "-S", socket,
		"display-message", "-p", "-t", "cli:0.0", "#{window_width}x#{window_height}"))
	if before != "120x40" || after != before {
		t.Fatalf("geometry changed by read-only observer: before=%q after=%q", before, after)
	}
	waitForUpdatesQuiet(t, ctx, observer.Updates(), 50*time.Millisecond)
	runTmux(t, ctx, environment, tmux, "-u", "-N", "-S", socket,
		"send-keys", "-l", "-t", "cli:0.0", "--", "observer-output")
	select {
	case _, ok := <-observer.Updates():
		if !ok {
			t.Fatal("observer ended before output hint")
		}
	case <-ctx.Done():
		t.Fatal("tmux output did not produce a hint")
	}
	runTmux(t, ctx, environment, tmux, "-u", "-N", "-S", socket, "kill-pane", "-t", "cli:0.0")
	if err := awaitDone(t, observer); err != nil {
		t.Fatalf("pane close result=%v", err)
	}
}

func runTmux(t *testing.T, ctx context.Context, environment []string, executable string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		t.Fatalf("tmux %s failed: %v", arguments[len(arguments)-1], err)
	}
	return string(output)
}

func waitForUpdatesQuiet(t *testing.T, ctx context.Context, updates <-chan struct{}, quiet time.Duration) {
	t.Helper()
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-updates:
			if !ok {
				t.Fatal("observer ended while waiting for attach events to settle")
			}
			if !timer.Stop() {
				<-timer.C
			}
			timer.Reset(quiet)
		case <-timer.C:
			return
		case <-ctx.Done():
			t.Fatal("observer did not settle")
		}
	}
}

func withoutEnvironment(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
