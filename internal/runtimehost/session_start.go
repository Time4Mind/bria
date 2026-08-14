package runtimehost

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
)

// Start creates or resumes a provider in the deterministic Bria tmux window.
// Repeated calls are safe: an existing target is treated as success.
func (r *TmuxRecoveryRuntime) Start(ctx context.Context, session domain.Session) (string, error) {
	if session.RuntimeGeneration == 0 || strings.TrimSpace(session.Workdir) == "" {
		return "", errors.New("session runtime metadata is required")
	}
	workdir, err := filepath.Abs(session.Workdir)
	if err != nil {
		return "", fmt.Errorf("resolve session workdir: %w", err)
	}
	backend, ok := r.backends[strings.ToLower(session.Backend)]
	if !ok {
		return "", fmt.Errorf("unsupported session backend: %q", session.Backend)
	}
	tmuxPath, err := r.runner.LookPath("tmux")
	if err != nil {
		return "", fmt.Errorf("find tmux: %w", err)
	}
	backendPath, err := r.runner.LookPath(backend.Executable)
	if err != nil {
		return "", fmt.Errorf("find backend %s: %w", session.Backend, err)
	}
	window := TmuxWindowName(string(session.NodeID), string(session.ID))
	target := r.tmuxSession + ":" + window
	if exists, checkErr := r.windowExists(ctx, tmuxPath, target); checkErr != nil {
		return "", checkErr
	} else if exists {
		return target, nil
	}
	if err := r.ensureSession(ctx, tmuxPath); err != nil {
		return "", err
	}
	providerArgs, err := startArgs(session, backend.Flags)
	if err != nil {
		return "", err
	}
	args := []string{"new-window", "-a", "-d", "-t", r.tmuxSession}
	args = append(args, providerEnvironment(session, backend.Flags, r.tmuxSession, window)...)
	args = append(args, "-n", window, "-c", workdir, backendPath)
	args = append(args, providerArgs...)
	result, err := r.runner.Run(ctx, tmuxPath, args...)
	if err != nil {
		return "", fmt.Errorf("create session window: %w", err)
	}
	if result.ExitCode != 0 {
		if exists, checkErr := r.windowExists(ctx, tmuxPath, target); checkErr == nil && exists {
			return target, nil
		}
		return "", commandExitError("create session window", result)
	}
	if err := r.awaitProviderStartup(ctx, tmuxPath, target); err != nil {
		return "", err
	}
	return target, nil
}

func providerEnvironment(
	session domain.Session,
	flags []string,
	tmuxSession string,
	window string,
) []string {
	if strings.EqualFold(session.Backend, "claude") {
		for _, flag := range flags {
			if flag == "--dangerously-skip-permissions" {
				// Claude refuses its explicit bypass flag under root unless the
				// caller declares the already trusted/sandboxed execution context.
				return []string{"-e", "IS_SANDBOX=1"}
			}
		}
		return nil
	}
	if strings.EqualFold(session.Backend, "codex") {
		return []string{
			"-e", providerbinding.EnvNodeID + "=" + string(session.NodeID),
			"-e", providerbinding.EnvSessionID + "=" + string(session.ID),
			"-e", providerbinding.EnvTmuxSession + "=" + tmuxSession,
			"-e", providerbinding.EnvTmuxWindow + "=" + window,
		}
	}
	return nil
}

func startArgs(session domain.Session, flags []string) ([]string, error) {
	args := append([]string(nil), flags...)
	if session.ProviderResume {
		return resumeArgs(session.Backend, args, session.ProviderSessionID)
	}
	switch strings.ToLower(session.Backend) {
	case "claude":
		if strings.TrimSpace(session.ProviderSessionID) == "" {
			return nil, errors.New("assigned Claude session id is required")
		}
		return append(args, "--session-id", session.ProviderSessionID), nil
	case "codex":
		return args, nil
	default:
		return nil, fmt.Errorf("unsupported session backend: %q", session.Backend)
	}
}
