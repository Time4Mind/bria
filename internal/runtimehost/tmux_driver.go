package runtimehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

type TerminalOpener interface {
	OpenTerminal(context.Context, string) error
}

type TmuxDriver struct {
	runner      InputCommandRunner
	tmuxPath    string
	timeout     time.Duration
	submitDelay time.Duration
	terminal    TerminalOpener
}

const maxPaneCaptureBytes = 48 << 10

func NewTmuxDriver(
	runner InputCommandRunner,
	timeout time.Duration,
	submitDelay time.Duration,
	terminal TerminalOpener,
) (*TmuxDriver, error) {
	if runner == nil {
		return nil, errors.New("input command runner is required")
	}
	if timeout <= 0 || submitDelay < 0 || submitDelay >= timeout {
		return nil, errors.New("tmux timeout and submit delay are invalid")
	}
	path, err := runner.LookPath("tmux")
	if err != nil {
		return nil, fmt.Errorf("find tmux: %w", err)
	}
	return &TmuxDriver{
		runner: runner, tmuxPath: path, timeout: timeout,
		submitDelay: submitDelay, terminal: terminal,
	}, nil
}

func (d *TmuxDriver) SendLiteral(
	ctx context.Context,
	target string,
	operationID string,
	text string,
) error {
	if target == "" || operationID == "" {
		return errors.New("tmux target and operation id are required")
	}
	buffer := tmuxBufferName(operationID)
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	result, err := d.runner.RunInput(
		runCtx, []byte(text), d.tmuxPath, "load-buffer", "-b", buffer, "-",
	)
	if err != nil {
		return fmt.Errorf("load tmux input buffer: %w", err)
	}
	if result.ExitCode != 0 {
		return commandExitError("load tmux input buffer", result)
	}
	result, err = d.runner.Run(
		runCtx, d.tmuxPath, "paste-buffer", "-d", "-b", buffer, "-t", target,
	)
	if err != nil {
		return fmt.Errorf("paste tmux input buffer: %w", err)
	}
	if result.ExitCode != 0 {
		d.deleteBuffer(runCtx, buffer)
		return commandExitError("paste tmux input buffer", result)
	}
	if err := waitContext(runCtx, d.submitDelay); err != nil {
		return err
	}
	return d.sendKey(runCtx, target, "Enter")
}

func (d *TmuxDriver) SendKey(ctx context.Context, target, key string) error {
	if target == "" || key == "" {
		return errors.New("tmux target and key are required")
	}
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.sendKey(runCtx, target, key)
}

func (d *TmuxDriver) Close(ctx context.Context, target string) error {
	if target == "" {
		return errors.New("tmux target is required")
	}
	return d.run(ctx, "close tmux runtime", "kill-window", "-t", target)
}

func (d *TmuxDriver) OpenTerminal(ctx context.Context, target string) error {
	if d.terminal == nil {
		return ErrTerminalUnavailable
	}
	return d.terminal.OpenTerminal(ctx, target)
}

func (d *TmuxDriver) CapturePane(ctx context.Context, target string) ([]byte, error) {
	if target == "" {
		return nil, errors.New("tmux target is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	result, err := d.runner.Run(
		runCtx, d.tmuxPath, "capture-pane", "-e", "-p", "-t", target,
	)
	if err != nil {
		return nil, fmt.Errorf("capture tmux pane: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, commandExitError("capture tmux pane", result)
	}
	pane := result.Stdout
	if len(pane) > maxPaneCaptureBytes {
		pane = pane[len(pane)-maxPaneCaptureBytes:]
	}
	return append([]byte(nil), pane...), nil
}

func (d *TmuxDriver) TargetExists(ctx context.Context, target string) (bool, error) {
	if target == "" {
		return false, errors.New("tmux target is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	result, err := d.runner.Run(runCtx, d.tmuxPath, "has-session", "-t", target)
	if err != nil {
		return false, fmt.Errorf("inspect tmux runtime: %w", err)
	}
	return result.ExitCode == 0, nil
}

// ResizeViewport applies the provider viewport to an already attached runtime.
// This is intentionally separate from TargetExists so periodic health probes
// stay read-only.
func (d *TmuxDriver) ResizeViewport(ctx context.Context, target string) error {
	if target == "" {
		return errors.New("tmux target is required")
	}
	return d.run(
		ctx, "resize tmux viewport", "resize-window", "-t", target,
		"-x", fmt.Sprint(providerWindowWidth), "-y", fmt.Sprint(providerWindowHeight),
	)
}

func (d *TmuxDriver) sendKey(ctx context.Context, target, key string) error {
	result, err := d.runner.Run(ctx, d.tmuxPath, "send-keys", "-t", target, key)
	if err != nil {
		return fmt.Errorf("send tmux key: %w", err)
	}
	if result.ExitCode != 0 {
		return commandExitError("send tmux key", result)
	}
	return nil
}

func (d *TmuxDriver) run(ctx context.Context, action string, args ...string) error {
	runCtx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	result, err := d.runner.Run(runCtx, d.tmuxPath, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	if result.ExitCode != 0 {
		return commandExitError(action, result)
	}
	return nil
}

func (d *TmuxDriver) deleteBuffer(ctx context.Context, buffer string) {
	_, _ = d.runner.Run(ctx, d.tmuxPath, "delete-buffer", "-b", buffer)
}

func tmuxBufferName(operationID string) string {
	digest := sha256.Sum256([]byte(operationID))
	return "bria-" + hex.EncodeToString(digest[:12])
}

func waitContext(ctx context.Context, delay time.Duration) error {
	if delay == 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
