package runtimehost

import (
	"context"
	"fmt"
	"time"
)

var tmuxCapabilities = []Capability{
	CapabilityTmuxWindows,
	CapabilityTmuxPersistent,
	CapabilityTmuxLiteralInput,
	CapabilityTmuxInteractiveKeys,
	CapabilityTmuxANSICapture,
}

type TmuxDescriptor struct {
	Executable   string
	Version      string
	Available    bool
	Capabilities []Capability
}

type TmuxProbe struct {
	runner  CommandRunner
	timeout time.Duration
}

func NewTmuxProbe(runner CommandRunner, timeout time.Duration) *TmuxProbe {
	return &TmuxProbe{runner: runner, timeout: timeout}
}

func (p *TmuxProbe) Probe(ctx context.Context) (TmuxDescriptor, error) {
	descriptor := TmuxDescriptor{}
	if p == nil || p.runner == nil {
		return descriptor, fmt.Errorf("tmux probe command runner is required")
	}
	if p.timeout <= 0 {
		return descriptor, fmt.Errorf("tmux probe timeout must be positive")
	}
	path, err := p.runner.LookPath("tmux")
	if err != nil {
		return descriptor, fmt.Errorf("tmux executable is unavailable: %w", err)
	}
	descriptor.Executable = path
	commandCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	result, err := p.runner.Run(commandCtx, path, "-V")
	if err != nil {
		return descriptor, fmt.Errorf("probe tmux version: %w", err)
	}
	if result.ExitCode != 0 {
		return descriptor, fmt.Errorf(
			"probe tmux version exited with %d: %s",
			result.ExitCode,
			boundedDetail(result.Stderr),
		)
	}
	version := firstLine(result.Stdout)
	if version == "" {
		return descriptor, fmt.Errorf("tmux returned an empty version")
	}
	descriptor.Available = true
	descriptor.Version = version
	descriptor.Capabilities = append([]Capability(nil), tmuxCapabilities...)
	return descriptor, nil
}
