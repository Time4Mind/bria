package quota

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

const claudeQuotaWindow = "__bria_quota_claude__"

type ClaudeCollector struct {
	runner      runtimehost.CommandRunner
	tmuxSession string
	command     string
	flags       []string
}

func NewClaudeCollector(
	runner runtimehost.CommandRunner,
	tmuxSession string,
	command string,
	flags []string,
) (*ClaudeCollector, error) {
	if runner == nil || strings.TrimSpace(tmuxSession) == "" || strings.TrimSpace(command) == "" {
		return nil, errors.New("Claude quota runner, tmux session, and command are required")
	}
	return &ClaudeCollector{
		runner: runner, tmuxSession: tmuxSession, command: command,
		flags: append([]string(nil), flags...),
	}, nil
}

func (c *ClaudeCollector) Backend() string { return "claude" }

func (c *ClaudeCollector) Collect(ctx context.Context, nodeID domain.NodeID) (domain.QuotaSnapshot, error) {
	tmuxPath, err := c.runner.LookPath("tmux")
	if err != nil {
		return domain.QuotaSnapshot{}, err
	}
	if err := c.ensureWindow(ctx, tmuxPath); err != nil {
		return domain.QuotaSnapshot{}, err
	}
	snapshot, err := c.poll(ctx, tmuxPath, nodeID)
	if err == nil {
		return snapshot, nil
	}
	// A parked Claude process can become wedged. This window is dedicated to
	// quota reads, so recreating only it is safe and mirrors CCBot's recovery.
	target := c.tmuxSession + ":" + claudeQuotaWindow
	_, _ = c.runner.Run(ctx, tmuxPath, "kill-window", "-t", target)
	if createErr := c.ensureWindow(ctx, tmuxPath); createErr != nil {
		return domain.QuotaSnapshot{}, createErr
	}
	return c.poll(ctx, tmuxPath, nodeID)
}

func (c *ClaudeCollector) poll(
	ctx context.Context,
	tmuxPath string,
	nodeID domain.NodeID,
) (domain.QuotaSnapshot, error) {
	target := c.tmuxSession + ":" + claudeQuotaWindow
	_, _ = c.runner.Run(ctx, tmuxPath, "send-keys", "-t", target, "Escape")
	_, _ = c.runner.Run(ctx, tmuxPath, "clear-history", "-t", target)
	if result, runErr := c.runner.Run(ctx, tmuxPath, "send-keys", "-t", target, "-l", "/usage"); runErr != nil || result.ExitCode != 0 {
		return domain.QuotaSnapshot{}, errors.New("send Claude usage command")
	}
	_, _ = c.runner.Run(ctx, tmuxPath, "send-keys", "-t", target, "Enter")
	var last string
	for attempt := 0; attempt < 60; attempt++ {
		select {
		case <-ctx.Done():
			return domain.QuotaSnapshot{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		result, captureErr := c.runner.Run(ctx, tmuxPath, "capture-pane", "-p", "-S", "-100", "-t", target)
		if captureErr != nil || result.ExitCode != 0 {
			continue
		}
		snapshot, ok := ParseClaudeUsage(string(result.Stdout), nodeID, time.Now())
		if !ok {
			continue
		}
		fingerprint := fmt.Sprintf("%v:%v", snapshot.FiveHour, snapshot.Weekly)
		if fingerprint == last {
			_, _ = c.runner.Run(ctx, tmuxPath, "send-keys", "-t", target, "Escape")
			return snapshot, nil
		}
		last = fingerprint
	}
	return domain.QuotaSnapshot{}, errors.New("Claude usage modal did not settle")
}

func (c *ClaudeCollector) ensureWindow(ctx context.Context, tmuxPath string) error {
	target := c.tmuxSession + ":" + claudeQuotaWindow
	result, err := c.runner.Run(
		ctx, tmuxPath, "list-windows", "-t", c.tmuxSession, "-F", "#{window_name}",
	)
	if err == nil && result.ExitCode == 0 {
		for _, name := range strings.Split(string(result.Stdout), "\n") {
			if strings.TrimSpace(name) == claudeQuotaWindow {
				return c.confirmTrust(ctx, tmuxPath, target)
			}
		}
	}
	backendPath, err := c.runner.LookPath(c.command)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	args := []string{"new-window", "-d", "-t", c.tmuxSession, "-n", claudeQuotaWindow, "-c", home, backendPath}
	args = append(args, c.flags...)
	result, err = c.runner.Run(ctx, tmuxPath, args...)
	if err != nil || result.ExitCode != 0 {
		return errors.New("create Claude quota window")
	}
	if err := waitContext(ctx, 4*time.Second); err != nil {
		return err
	}
	if err := c.confirmTrust(ctx, tmuxPath, target); err != nil {
		return err
	}
	return c.warm(ctx, tmuxPath, target)
}

func (c *ClaudeCollector) confirmTrust(ctx context.Context, tmuxPath, target string) error {
	for attempt := 0; attempt < 12; attempt++ {
		pane, err := c.capture(ctx, tmuxPath, target)
		if err == nil {
			if strings.Contains(pane, "trust this folder") || strings.Contains(pane, "Is this a project you") {
				_, _ = c.runner.Run(ctx, tmuxPath, "send-keys", "-t", target, "Enter")
				return waitContext(ctx, 3*time.Second)
			}
			if strings.Contains(pane, "? for shortcuts") {
				return nil
			}
		}
		if err := waitContext(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("Claude quota window did not reach its prompt")
}

func (c *ClaudeCollector) warm(ctx context.Context, tmuxPath, target string) error {
	result, err := c.runner.Run(
		ctx, tmuxPath, "send-keys", "-t", target, "-l", "respond with exactly: ok",
	)
	if err != nil || result.ExitCode != 0 {
		return errors.New("warm Claude quota window")
	}
	_, _ = c.runner.Run(ctx, tmuxPath, "send-keys", "-t", target, "Enter")
	if err := waitContext(ctx, time.Second); err != nil {
		return err
	}
	for attempt := 0; attempt < 24; attempt++ {
		if err := waitContext(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		pane, captureErr := c.capture(ctx, tmuxPath, target)
		if captureErr == nil && !strings.Contains(pane, "esc to interrupt") {
			return nil
		}
	}
	return errors.New("Claude quota warm-up did not finish")
}

func (c *ClaudeCollector) capture(ctx context.Context, tmuxPath, target string) (string, error) {
	result, err := c.runner.Run(ctx, tmuxPath, "capture-pane", "-p", "-S", "-100", "-t", target)
	if err != nil || result.ExitCode != 0 {
		return "", errors.New("capture Claude quota window")
	}
	return string(result.Stdout), nil
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
