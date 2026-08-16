package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type backendCommandRunner struct{ available map[string]bool }

func (r backendCommandRunner) LookPath(name string) (string, error) {
	if r.available[name] {
		return name, nil
	}
	return "", errors.New("missing")
}

func (backendCommandRunner) Run(
	context.Context, string, ...string,
) (runtimehost.CommandResult, error) {
	return runtimehost.CommandResult{}, nil
}

func (backendCommandRunner) RunInput(
	context.Context, []byte, string, ...string,
) (runtimehost.CommandResult, error) {
	return runtimehost.CommandResult{}, nil
}

func (backendCommandRunner) RunJSONRPC(
	context.Context, []byte, []byte, int, string, ...string,
) (runtimehost.CommandResult, error) {
	return runtimehost.CommandResult{}, nil
}

func TestUnavailableInheritedBackendPathsUseNodeManagedCommands(t *testing.T) {
	runtime := backendRuntime{
		home: "/srv/bria-agent",
		runner: backendCommandRunner{available: map[string]bool{
			"/opt/custom/claude": true,
		}},
	}
	configured, roots := prepareManagedBackendCommands(config.Config{
		ClaudeCommand: "/opt/custom/claude",
		CodexCommand:  "/root/.local/bin/codex",
	}, runtime)
	if configured.ClaudeCommand != "/opt/custom/claude" {
		t.Fatalf("available custom command changed to %q", configured.ClaudeCommand)
	}
	want := filepath.Join(roots["codex"], "node_modules", ".bin", "codex")
	if configured.CodexCommand != want {
		t.Fatalf("managed codex command=%q want=%q", configured.CodexCommand, want)
	}
}
