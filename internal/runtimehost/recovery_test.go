package runtimehost

import (
	"context"
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
			command: BackendCommand{Executable: "claude", Flags: []string{"--flag"}},
			path:    "/opt/claude",
			want:    []string{"/opt/claude", "--flag", "--resume", "provider;not-shell"},
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
			if len(runner.calls) != 4 {
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
	if len(runner.calls) != 1 || runner.calls[0].args[0] != "has-session" {
		t.Fatalf("idempotent calls=%#v", runner.calls)
	}
}

func TestTmuxSessionRuntimeFreshAndResumeArgv(t *testing.T) {
	tests := []struct {
		name    string
		session domain.Session
		want    []string
	}{
		{name: "fresh claude", session: domain.Session{Backend: "claude", ProviderSessionID: "assigned"}, want: []string{"/claude", "--flag", "--session-id", "assigned"}},
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
				"claude": {Executable: "claude", Flags: []string{"--flag"}},
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
			args := runner.calls[len(runner.calls)-1].args
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
