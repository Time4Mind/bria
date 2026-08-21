package runtimehost

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type recordedCall struct {
	name string
	args []string
}

type scriptedRunner struct {
	paths   map[string]string
	results []CommandResult
	calls   []recordedCall
}

func (r *scriptedRunner) LookPath(name string) (string, error) {
	path, ok := r.paths[name]
	if !ok {
		return "", exec.ErrNotFound
	}
	return path, nil
}

func (r *scriptedRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) (CommandResult, error) {
	r.calls = append(r.calls, recordedCall{name: name, args: append([]string(nil), args...)})
	if len(r.results) == 0 {
		return CommandResult{}, nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func TestTmuxRecoveryRuntimeStartsBackendWithArgv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		backend string
		command BackendCommand
		path    string
		want    []string
	}{
		{
			backend: "claude",
			command: BackendCommand{Executable: "claude", Flags: []string{"--dangerously-skip-permissions"}},
			path:    "/opt/claude",
			want:    []string{"/opt/claude", "--dangerously-skip-permissions", "--resume", "provider;not-shell"},
		},
		{
			backend: "codex",
			command: BackendCommand{Executable: "codex", Flags: []string{"--no-alt-screen"}},
			path:    "/opt/codex",
			want:    []string{"/opt/codex", "--no-alt-screen", "resume", "provider;not-shell"},
		},
	}
	for _, test := range tests {
		t.Run(test.backend, func(t *testing.T) {
			workdir := t.TempDir()
			runner := &scriptedRunner{
				paths:   map[string]string{"tmux": "/usr/bin/tmux", test.command.Executable: test.path},
				results: []CommandResult{{ExitCode: 1}, {ExitCode: 1}, {}, {}},
			}
			runtime, err := NewTmuxRecoveryRuntime(
				runner, "bria", map[string]BackendCommand{test.backend: test.command}, time.Second,
			)
			if err != nil {
				t.Fatal(err)
			}
			session := domain.Session{
				ID: "s", NodeID: "n", Backend: test.backend, Workdir: workdir,
				ProviderSessionID: "provider;not-shell",
			}
			if err := runtime.Resume(context.Background(), session, "operation"); err != nil {
				t.Fatalf("Resume(): %v", err)
			}
			if len(runner.calls) != 6 {
				t.Fatalf("calls=%#v", runner.calls)
			}
			last := runner.calls[3]
			wantPrefix := []string{"new-window", "-a", "-d", "-t", "bria"}
			if last.name != "/usr/bin/tmux" || !reflect.DeepEqual(last.args[:len(wantPrefix)], wantPrefix) {
				t.Fatalf("new-window prefix = %q %#v", last.name, last.args)
			}
			backendIndex := slices.Index(last.args, test.path)
			if backendIndex < 2 || last.args[backendIndex-2] != "-c" || last.args[backendIndex-1] != workdir {
				t.Fatalf("new-window target = %#v", last.args)
			}
			if !reflect.DeepEqual(last.args[backendIndex:], test.want) {
				t.Fatalf("backend argv = %#v, want %#v", last.args[9:], test.want)
			}
			if test.backend == "codex" && !slices.Contains(last.args, "BRIA_BINDING_SESSION_ID=s") {
				t.Fatalf("Codex launch has no Bria binding identity: %#v", last.args)
			}
			if test.backend == "claude" && !slices.Contains(last.args, "IS_SANDBOX=1") {
				t.Fatalf("Claude launch has no root-compatible sandbox marker: %#v", last.args)
			}
			for _, variable := range []string{
				"BRIA_BINDING_NODE_ID=n",
				"BRIA_BINDING_SESSION_ID=s",
				"BRIA_BINDING_TMUX_SESSION=bria",
				"BRIA_BINDING_TMUX_WINDOW=" + TmuxWindowName("n", "s"),
			} {
				if !slices.Contains(last.args, variable) {
					t.Fatalf("provider launch has no %s: %#v", variable, last.args)
				}
			}
			resize := runner.calls[4]
			if !reflect.DeepEqual(resize.args, []string{
				"resize-window", "-t", "bria:" + TmuxWindowName("n", "s"),
				"-x", "80", "-y", "40",
			}) {
				t.Fatalf("provider window resize=%#v", resize.args)
			}
		})
	}
}

func TestTmuxRecoveryRuntimeReturnsExistingDeterministicWindow(t *testing.T) {
	runner := &scriptedRunner{
		paths:   map[string]string{"tmux": "/tmux", "codex": "/codex"},
		results: []CommandResult{{ExitCode: 0}},
	}
	runtime, err := NewTmuxRecoveryRuntime(
		runner, "bria", map[string]BackendCommand{"codex": {Executable: "codex"}}, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Resume(context.Background(), domain.Session{
		Backend: "codex", Workdir: t.TempDir(), ProviderSessionID: "provider",
	}, "same-operation"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[0].args[0] != "has-session" ||
		runner.calls[1].args[0] != "resize-window" {
		t.Fatalf("idempotent calls=%#v", runner.calls)
	}
}

func TestTmuxSessionRuntimeFreshAndResumeArgv(t *testing.T) {
	tests := []struct {
		name    string
		session domain.Session
		want    []string
	}{
		{name: "fresh claude", session: domain.Session{Backend: "claude", ProviderSessionID: "assigned"}, want: []string{"/claude", "--dangerously-skip-permissions", "--session-id", "assigned"}},
		{name: "fresh codex", session: domain.Session{Backend: "codex"}, want: []string{"/codex", "--flag"}},
		{name: "resume codex", session: domain.Session{Backend: "codex", ProviderSessionID: "existing", ProviderResume: true}, want: []string{"/codex", "--flag", "resume", "existing"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{
				paths:   map[string]string{"tmux": "/tmux", "claude": "/claude", "codex": "/codex"},
				results: []CommandResult{{ExitCode: 1}, {ExitCode: 0}, {ExitCode: 0}},
			}
			runtime, err := NewTmuxRecoveryRuntime(runner, "bria", map[string]BackendCommand{
				"claude": {Executable: "claude", Flags: []string{"--dangerously-skip-permissions"}},
				"codex":  {Executable: "codex", Flags: []string{"--flag"}},
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			test.session.ID, test.session.NodeID = "s", "n"
			test.session.Workdir, test.session.RuntimeGeneration = t.TempDir(), 1
			target, err := runtime.Start(context.Background(), test.session)
			if err != nil {
				t.Fatal(err)
			}
			if target != "bria:"+TmuxWindowName("n", "s") {
				t.Fatalf("target=%q", target)
			}
			var args []string
			for _, call := range runner.calls {
				if len(call.args) > 0 && call.args[0] == "new-window" {
					args = call.args
				}
			}
			if args == nil {
				t.Fatalf("new-window call missing from %#v", runner.calls)
			}
			backendPath := "/" + test.session.Backend
			backendIndex := slices.Index(args, backendPath)
			if backendIndex < 0 {
				t.Fatalf("provider executable missing from %#v", args)
			}
			if got := args[backendIndex:]; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("provider argv=%#v want=%#v", got, test.want)
			}
		})
	}
}

func TestTmuxSessionRuntimeRejectsProviderThatExitsDuringStartup(t *testing.T) {
	runner := &scriptedRunner{
		paths: map[string]string{"tmux": "/tmux", "claude": "/claude"},
		results: []CommandResult{
			{ExitCode: 1}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 1},
		},
	}
	runtime, err := NewTmuxRecoveryRuntime(runner, "bria", map[string]BackendCommand{
		"claude": {Executable: "claude", Flags: []string{"--dangerously-skip-permissions"}},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), domain.Session{
		ID: "s", NodeID: "n", Backend: "claude", Workdir: t.TempDir(),
		ProviderSessionID: "assigned", RuntimeGeneration: 1,
	})
	if err == nil || err.Error() != "provider exited during startup" {
		t.Fatalf("startup error=%v", err)
	}
}

func TestTmuxSessionRuntimeClassifiesCodexResumeStartupExit(t *testing.T) {
	runner := &scriptedRunner{
		paths: map[string]string{"tmux": "/tmux", "codex": "/codex"},
		results: []CommandResult{
			{ExitCode: 1}, {ExitCode: 0}, {ExitCode: 0}, {ExitCode: 1},
		},
	}
	runtime, err := NewTmuxRecoveryRuntime(runner, "bria", map[string]BackendCommand{
		"codex": {Executable: "codex"},
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), domain.Session{
		ID: "s", NodeID: "n", Backend: "codex", Workdir: t.TempDir(),
		ProviderSessionID: "ccbot-provider", ProviderResume: true, RuntimeGeneration: 1,
	})
	if !errors.Is(err, ErrProviderExitedDuringStartup) {
		t.Fatalf("startup error=%v, want provider-exited sentinel", err)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "resize-window" {
			t.Fatalf("dead Codex resume was resized before liveness probe: %#v", runner.calls)
		}
	}
}
