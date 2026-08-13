package runtimehost

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

type fakeRunner struct {
	paths  map[string]string
	result CommandResult
	err    error
	wait   bool
	name   string
	args   []string
}

func (runner *fakeRunner) LookPath(name string) (string, error) {
	path, ok := runner.paths[name]
	if !ok {
		return "", exec.ErrNotFound
	}
	return path, nil
}

func (runner *fakeRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	runner.name = name
	runner.args = append([]string(nil), args...)
	if runner.wait {
		<-ctx.Done()
		return CommandResult{}, ctx.Err()
	}
	return runner.result, runner.err
}

func TestBackendProbesReportVersionAndCapabilities(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		newProbe   func(CommandRunner, time.Duration) BackendProbe
		path       string
		version    string
		assignedID bool
	}{
		{
			name:       "claude",
			newProbe:   NewClaudeProbe,
			path:       "/opt/bin/claude",
			version:    "2.1.225 (Claude Code)",
			assignedID: true,
		},
		{
			name:       "codex",
			newProbe:   NewCodexProbe,
			path:       "/opt/bin/codex",
			version:    "codex-cli 0.146.1",
			assignedID: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				paths:  map[string]string{test.name: test.path},
				result: CommandResult{Stdout: []byte(test.version + "\nignored\n")},
			}
			descriptor, err := test.newProbe(runner, time.Second).Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe(): %v", err)
			}
			if !descriptor.Available || descriptor.Name != test.name || descriptor.Executable != test.path || descriptor.Version != test.version {
				t.Fatalf("descriptor = %#v", descriptor)
			}
			if runner.name != test.path || !reflect.DeepEqual(runner.args, []string{"--version"}) {
				t.Fatalf("command = %q %#v", runner.name, runner.args)
			}
			if containsCapability(descriptor.Capabilities, CapabilityAssignedProviderID) != test.assignedID {
				t.Fatalf("assigned provider ID capability = %v", descriptor.Capabilities)
			}
			for _, capability := range []Capability{CapabilitySessionCreate, CapabilitySessionResume, CapabilityLifecycleHooks, CapabilityJSONLTranscript} {
				if !containsCapability(descriptor.Capabilities, capability) {
					t.Fatalf("missing capability %q in %v", capability, descriptor.Capabilities)
				}
			}
		})
	}
}

func TestBackendProbeUnavailableAndTimeout(t *testing.T) {
	t.Parallel()
	t.Run("missing executable", func(t *testing.T) {
		descriptor, err := NewClaudeProbe(&fakeRunner{paths: map[string]string{}}, time.Second).Probe(context.Background())
		if !errors.Is(err, ErrBackendUnavailable) || descriptor.Available {
			t.Fatalf("Probe() = %#v, %v", descriptor, err)
		}
	})
	t.Run("version timeout", func(t *testing.T) {
		runner := &fakeRunner{paths: map[string]string{"codex": "/bin/codex"}, wait: true}
		_, err := NewCodexProbe(runner, time.Millisecond).Probe(context.Background())
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Probe() error = %v, want deadline exceeded", err)
		}
	})
}

func TestTmuxProbeReportsHostRuntimeCapabilities(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		paths:  map[string]string{"tmux": "/usr/bin/tmux"},
		result: CommandResult{Stdout: []byte("tmux 3.7b\n")},
	}
	descriptor, err := NewTmuxProbe(runner, time.Second).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe(): %v", err)
	}
	if !descriptor.Available || descriptor.Version != "tmux 3.7b" {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	for _, capability := range tmuxCapabilities {
		if !containsCapability(descriptor.Capabilities, capability) {
			t.Fatalf("missing capability %q", capability)
		}
	}
}

func containsCapability(capabilities []Capability, wanted Capability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}
