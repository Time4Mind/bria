package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type BackendCommand struct {
	Executable string
	Flags      []string
}

const (
	providerStartupGrace = 250 * time.Millisecond
	providerWindowWidth  = 80
	providerWindowHeight = 40
)

// TmuxRecoveryRuntime restores a provider session into a deterministic tmux
// window. All command components are passed as argv; session metadata is never
// interpolated into a shell command.
type TmuxRecoveryRuntime struct {
	runner      CommandRunner
	tmuxSession string
	backends    map[string]BackendCommand
	timeout     time.Duration
}

func NewTmuxRecoveryRuntime(
	runner CommandRunner,
	tmuxSession string,
	backends map[string]BackendCommand,
	timeout time.Duration,
) (*TmuxRecoveryRuntime, error) {
	if runner == nil {
		return nil, errors.New("command runner is required")
	}
	if strings.TrimSpace(tmuxSession) == "" {
		return nil, errors.New("tmux session is required")
	}
	if timeout <= 0 {
		return nil, errors.New("runtime timeout must be positive")
	}
	commands := make(map[string]BackendCommand, len(backends))
	for name, command := range backends {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" || strings.TrimSpace(command.Executable) == "" {
			return nil, errors.New("backend name and executable are required")
		}
		command.Flags = append([]string(nil), command.Flags...)
		commands[name] = command
	}
	return &TmuxRecoveryRuntime{
		runner: runner, tmuxSession: tmuxSession, backends: commands, timeout: timeout,
	}, nil
}

func (r *TmuxRecoveryRuntime) Resume(
	ctx context.Context,
	session domain.Session,
	operationID string,
) error {
	if strings.TrimSpace(session.ProviderSessionID) == "" {
		return errors.New("provider session id is required")
	}
	if strings.TrimSpace(session.Workdir) == "" {
		return errors.New("session workdir is required")
	}
	if strings.TrimSpace(operationID) == "" {
		return errors.New("operation id is required")
	}
	workdir, err := filepath.Abs(session.Workdir)
	if err != nil {
		return fmt.Errorf("resolve session workdir: %w", err)
	}
	info, err := os.Stat(workdir)
	if err != nil {
		return fmt.Errorf("inspect session workdir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("session workdir is not a directory")
	}
	backend, ok := r.backends[strings.ToLower(session.Backend)]
	if !ok {
		return fmt.Errorf("unsupported session backend: %q", session.Backend)
	}
	tmuxPath, err := r.runner.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("find tmux: %w", err)
	}
	backendPath, err := r.runner.LookPath(backend.Executable)
	if err != nil {
		return fmt.Errorf("find backend %s: %w", session.Backend, err)
	}

	windowName := TmuxWindowName(string(session.NodeID), string(session.ID))
	target := r.tmuxSession + ":" + windowName
	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if exists, err := r.windowExists(runCtx, tmuxPath, target); err != nil {
		return err
	} else if exists {
		return r.resizeProviderWindow(runCtx, tmuxPath, target)
	}
	if err := r.ensureSession(runCtx, tmuxPath); err != nil {
		return err
	}

	providerArgs, err := resumeArgs(session.Backend, backend.Flags, session.ProviderSessionID)
	if err != nil {
		return err
	}
	args := []string{"new-window", "-a", "-d", "-t", r.tmuxSession}
	args = append(args, providerEnvironment(session, backend.Flags, r.tmuxSession, windowName)...)
	args = append(args, "-n", windowName, "-c", workdir, backendPath)
	args = append(args, providerArgs...)
	result, err := r.runner.Run(runCtx, tmuxPath, args...)
	if err != nil {
		return fmt.Errorf("create recovery window: %w", err)
	}
	if result.ExitCode != 0 {
		// A concurrent retry may have created the deterministic window first.
		if exists, checkErr := r.windowExists(runCtx, tmuxPath, target); checkErr == nil && exists {
			return nil
		}
		return commandExitError("create recovery window", result)
	}
	if err := r.resizeProviderWindow(runCtx, tmuxPath, target); err != nil {
		return err
	}
	return r.awaitProviderStartup(runCtx, tmuxPath, target)
}

func (r *TmuxRecoveryRuntime) resizeProviderWindow(
	ctx context.Context,
	tmuxPath string,
	target string,
) error {
	result, err := r.runner.Run(
		ctx, tmuxPath, "resize-window", "-t", target,
		"-x", fmt.Sprint(providerWindowWidth), "-y", fmt.Sprint(providerWindowHeight),
	)
	if err != nil {
		return fmt.Errorf("resize provider window: %w", err)
	}
	if result.ExitCode != 0 {
		return commandExitError("resize provider window", result)
	}
	return nil
}

func (r *TmuxRecoveryRuntime) awaitProviderStartup(
	ctx context.Context,
	tmuxPath string,
	target string,
) error {
	timer := time.NewTimer(providerStartupGrace)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	exists, err := r.windowExists(ctx, tmuxPath, target)
	if err != nil {
		return err
	}
	if !exists {
		return ErrProviderExitedDuringStartup
	}
	return nil
}

func (r *TmuxRecoveryRuntime) ensureSession(ctx context.Context, tmuxPath string) error {
	result, err := r.runner.Run(ctx, tmuxPath, "has-session", "-t", r.tmuxSession)
	if err != nil {
		return fmt.Errorf("inspect tmux session: %w", err)
	}
	if result.ExitCode == 0 {
		return nil
	}
	result, err = r.runner.Run(
		ctx, tmuxPath, "new-session", "-d", "-s", r.tmuxSession, "-n", "__bria__",
	)
	if err != nil {
		return fmt.Errorf("ensure tmux session: %w", err)
	}
	if result.ExitCode != 0 {
		// Another recovery worker may have created the shared tmux session.
		check, checkErr := r.runner.Run(ctx, tmuxPath, "has-session", "-t", r.tmuxSession)
		if checkErr == nil && check.ExitCode == 0 {
			return nil
		}
		return commandExitError("ensure tmux session", result)
	}
	return nil
}

func (r *TmuxRecoveryRuntime) windowExists(
	ctx context.Context,
	tmuxPath string,
	target string,
) (bool, error) {
	result, err := r.runner.Run(ctx, tmuxPath, "has-session", "-t", target)
	if err != nil {
		return false, fmt.Errorf("inspect recovery window: %w", err)
	}
	return result.ExitCode == 0, nil
}

func resumeArgs(backend string, flags []string, providerSessionID string) ([]string, error) {
	args := append([]string(nil), flags...)
	switch strings.ToLower(backend) {
	case "claude":
		return append(args, "--resume", providerSessionID), nil
	case "codex":
		return append(args, "resume", providerSessionID), nil
	default:
		return nil, fmt.Errorf("unsupported session backend: %q", backend)
	}
}

func commandExitError(action string, result CommandResult) error {
	detail := boundedDetail(result.Stderr)
	return fmt.Errorf("%s exited with %d: %s", action, result.ExitCode, detail)
}
