package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrBackendUnavailable = errors.New("backend executable is unavailable")

type Capability string

const (
	CapabilitySessionCreate       Capability = "session.create"
	CapabilitySessionResume       Capability = "session.resume"
	CapabilityInteractiveInput    Capability = "session.interactive_input"
	CapabilityLifecycleHooks      Capability = "lifecycle.hooks"
	CapabilityJSONLTranscript     Capability = "transcript.jsonl"
	CapabilityAssignedProviderID  Capability = "provider_session_id.assign"
	CapabilityTmuxWindows         Capability = "tmux.windows"
	CapabilityTmuxPersistent      Capability = "tmux.persistent_server"
	CapabilityTmuxLiteralInput    Capability = "tmux.literal_input"
	CapabilityTmuxInteractiveKeys Capability = "tmux.interactive_keys"
	CapabilityTmuxANSICapture     Capability = "tmux.capture_ansi"
)

type BackendDescriptor struct {
	Name         string
	Executable   string
	Version      string
	Available    bool
	Capabilities []Capability
}

type BackendProbe interface {
	Name() string
	Probe(context.Context) (BackendDescriptor, error)
}

type executableBackendProbe struct {
	name         string
	executable   string
	versionArgs  []string
	capabilities []Capability
	runner       CommandRunner
	timeout      time.Duration
}

func NewClaudeProbe(runner CommandRunner, timeout time.Duration) BackendProbe {
	return &executableBackendProbe{
		name:        "claude",
		executable:  "claude",
		versionArgs: []string{"--version"},
		capabilities: []Capability{
			CapabilitySessionCreate,
			CapabilitySessionResume,
			CapabilityInteractiveInput,
			CapabilityLifecycleHooks,
			CapabilityJSONLTranscript,
			CapabilityAssignedProviderID,
		},
		runner:  runner,
		timeout: timeout,
	}
}

func NewCodexProbe(runner CommandRunner, timeout time.Duration) BackendProbe {
	return &executableBackendProbe{
		name:        "codex",
		executable:  "codex",
		versionArgs: []string{"--version"},
		capabilities: []Capability{
			CapabilitySessionCreate,
			CapabilitySessionResume,
			CapabilityInteractiveInput,
			CapabilityLifecycleHooks,
			CapabilityJSONLTranscript,
		},
		runner:  runner,
		timeout: timeout,
	}
}

func (p *executableBackendProbe) Name() string {
	return p.name
}

func (p *executableBackendProbe) Probe(ctx context.Context) (BackendDescriptor, error) {
	descriptor := BackendDescriptor{Name: p.name}
	if p.runner == nil {
		return descriptor, fmt.Errorf("probe %s: command runner is required", p.name)
	}
	if p.timeout <= 0 {
		return descriptor, fmt.Errorf("probe %s: timeout must be positive", p.name)
	}
	path, err := p.runner.LookPath(p.executable)
	if err != nil {
		return descriptor, fmt.Errorf("%w: %s: %v", ErrBackendUnavailable, p.name, err)
	}
	descriptor.Executable = path
	commandCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	result, err := p.runner.Run(commandCtx, path, p.versionArgs...)
	if err != nil {
		return descriptor, fmt.Errorf("probe %s version: %w", p.name, err)
	}
	if result.ExitCode != 0 {
		return descriptor, fmt.Errorf(
			"probe %s version exited with %d: %s",
			p.name,
			result.ExitCode,
			boundedDetail(result.Stderr),
		)
	}
	version := firstLine(result.Stdout)
	if version == "" {
		version = firstLine(result.Stderr)
	}
	if version == "" {
		return descriptor, fmt.Errorf("probe %s returned an empty version", p.name)
	}
	descriptor.Available = true
	descriptor.Version = version
	descriptor.Capabilities = append([]Capability(nil), p.capabilities...)
	return descriptor, nil
}

func firstLine(value []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(value)), "\n")
	line = strings.TrimSpace(line)
	if len(line) > 256 {
		return line[:256]
	}
	return line
}

func boundedDetail(value []byte) string {
	detail := firstLine(value)
	if detail == "" {
		return "command failed"
	}
	return detail
}
