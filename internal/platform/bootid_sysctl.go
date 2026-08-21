package platform

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var darwinBootTimePattern = regexp.MustCompile(`sec\s*=\s*([0-9]+)\s*,\s*usec\s*=\s*([0-9]+)`)

type CommandResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(context.Context, string, ...string) (CommandResult, error)
}

type DarwinBootIDProvider struct {
	runner  CommandRunner
	timeout time.Duration
}

func NewDarwinBootIDProvider(runner CommandRunner, timeout time.Duration) *DarwinBootIDProvider {
	return &DarwinBootIDProvider{runner: runner, timeout: timeout}
}

func (p *DarwinBootIDProvider) Current(ctx context.Context) (string, error) {
	if p == nil || p.runner == nil {
		return "", fmt.Errorf("darwin boot id command runner is required")
	}
	if p.timeout <= 0 {
		return "", fmt.Errorf("darwin boot id timeout must be positive")
	}
	commandCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	result, err := p.runner.Run(commandCtx, "sysctl", "-n", "kern.boottime")
	if err != nil {
		return "", fmt.Errorf("read darwin boot time: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("sysctl kern.boottime exited with %d", result.ExitCode)
	}
	matches := darwinBootTimePattern.FindSubmatch(result.Stdout)
	if len(matches) != 3 {
		return "", fmt.Errorf("cannot parse darwin kern.boottime")
	}
	seconds, err := strconv.ParseUint(string(matches[1]), 10, 64)
	if err != nil {
		return "", fmt.Errorf("parse darwin boot seconds: %w", err)
	}
	microseconds, err := strconv.ParseUint(string(matches[2]), 10, 32)
	if err != nil || microseconds >= 1_000_000 {
		return "", fmt.Errorf("parse darwin boot microseconds")
	}
	// kern.boottime's microsecond component is not stable across macOS daemon
	// restarts. The second is stable for the host boot and avoids turning a
	// routine Bria update into a false reboot recovery.
	return fmt.Sprintf("darwin:%d", seconds), nil
}
