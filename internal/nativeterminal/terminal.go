// Package nativeterminal owns an isolated, foreground tmux server for one CLI.
// It never connects to the user's tmux server or interprets input as shell code.
package nativeterminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const operationTimeout = 5 * time.Second

// Match CCBot's paste/Enter separation: successful tmux delivery does not mean
// the CLI composer has consumed the input; rapid Enter can become pasted text.
// Navigation keys do not use this input-only settling interval.
const pasteSettle = 500 * time.Millisecond

type Config struct {
	Command     []string
	Workdir     string
	Environment []string
	// SocketDir is the parent of a new private directory, never an existing socket.
	SocketDir string
	// LiteralInputBarrier terminates Codex autocomplete tokens without changing
	// canonical request text (Codex trims this transport-only trailing space).
	LiteralInputBarrier bool
}

type Terminal struct {
	mu                  sync.Mutex
	path, dir, socket   string
	server              *exec.Cmd
	done                chan struct{}
	closed              bool
	closeErr            error
	process             *ownedProcess
	literalInputBarrier bool
}

func Open(ctx context.Context, config Config) (terminal *Terminal, err error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, errors.New("native terminal exact cleanup requires Linux amd64/arm64")
	}
	if len(config.Command) == 0 || config.Command[0] == "" {
		return nil, errors.New("native terminal command required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Workdir != "" {
		info, statErr := os.Stat(config.Workdir)
		if statErr != nil {
			return nil, statErr
		}
		if !info.IsDir() {
			return nil, errors.New("native terminal workdir is not a directory")
		}
	}
	path, err := exec.LookPath("tmux")
	if err != nil {
		return nil, errors.New("native terminal requires tmux")
	}
	envPath, err := exec.LookPath("env")
	if err != nil {
		return nil, errors.New("native terminal requires env")
	}
	dir, err := os.MkdirTemp(config.SocketDir, "bria-terminal-")
	if err != nil {
		return nil, err
	}
	t := &Terminal{path: path, dir: dir, socket: filepath.Join(dir, "socket"), done: make(chan struct{}), literalInputBarrier: config.LiteralInputBarrier}
	defer func() {
		if err != nil {
			err = errors.Join(err, t.Close(context.Background()))
		}
	}()
	t.server = exec.Command(path, "-u", "-D", "-f", os.DevNull, "-S", t.socket)
	t.server.Env = append([]string{}, config.Environment...)
	// Nested tmux environment must not redirect or constrain this private server.
	t.server.Env = append(t.server.Env, "TMUX=", "TERM=xterm-256color")
	if err = t.server.Start(); err != nil {
		t.server = nil
		close(t.done)
		return nil, err
	}
	go func() { _ = t.server.Wait(); close(t.done) }()
	startup, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	for {
		if _, statErr := os.Stat(t.socket); statErr == nil {
			break
		}
		select {
		case <-t.done:
			return nil, errors.New("native terminal server exited during startup")
		case <-startup.Done():
			return nil, startup.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	args := []string{"new-session", "-d", "-s", "cli", "-x", "120", "-y", "40"}
	if config.Workdir != "" {
		args = append(args, "-c", config.Workdir)
	}
	// Multiple argv operands tell tmux to exec directly. env also makes a
	// single executable (no arguments) unambiguously bypass shell parsing.
	args = append(args, "--", envPath, "--")
	args = append(args, config.Command...)
	if _, err = t.run(startup, nil, args...); err != nil {
		return nil, err
	}
	pidText, pidErr := t.run(startup, nil, "display-message", "-p", "-t", "cli:0.0", "#{pane_pid}")
	if pidErr != nil {
		return nil, pidErr
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(pidText))
	if parseErr != nil {
		return nil, errors.New("native terminal invalid pane identity")
	}
	t.process, err = ownProcess(pid)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Terminal) run(ctx context.Context, input []byte, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.path, append([]string{"-u", "-S", t.socket}, args...)...)
	cmd.Stdin = bytes.NewReader(input)
	var output limitedOutput
	cmd.Stdout = &output
	// CLI content and environment can be sensitive: do not include stderr in errors.
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("native terminal %s failed: %w", args[0], err)
	}
	return output.String(), nil
}

func (t *Terminal) Capture(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return "", errors.New("native terminal closed")
	}
	// Current screen only: no stale scrollback mixed into an interactive menu.
	return t.run(ctx, nil, "capture-pane", "-p", "-t", "cli:0.0")
}

func (t *Terminal) Input(ctx context.Context, text string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("native terminal closed")
	}
	if strings.IndexByte(text, 0) >= 0 {
		return errors.New("native terminal input contains NUL")
	}
	if t.literalInputBarrier {
		text += " "
	}
	if _, err := t.run(ctx, []byte(text), "load-buffer", "-b", "input", "-"); err != nil {
		return err
	}
	// Match the legacy literal transport: bracketed-paste (-p) leaves Codex's
	// slash command in paste/composer state instead of dispatching it on Enter.
	if _, err := t.run(ctx, nil, "paste-buffer", "-d", "-b", "input", "-t", "cli:0.0"); err != nil {
		return err
	}
	timer := time.NewTimer(pasteSettle)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	_, err := t.run(ctx, nil, "send-keys", "-t", "cli:0.0", "Enter")
	return err
}

func (t *Terminal) Key(ctx context.Context, key string) error {
	keys := map[string]string{"Up": "Up", "Down": "Down", "Left": "Left", "Right": "Right", "Enter": "Enter", "Escape": "Escape", "Tab": "Tab", "Space": "Space", "CtrlC": "C-c"}
	value, ok := keys[key]
	if !ok {
		return errors.New("unsupported native terminal key")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return errors.New("native terminal closed")
	}
	_, err := t.run(ctx, nil, "send-keys", "-t", "cli:0.0", value)
	return err
}

func (t *Terminal) Alive(ctx context.Context) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false, nil
	}
	select {
	case <-t.done:
		return false, nil
	default:
	}
	value, err := t.run(ctx, nil, "display-message", "-p", "-t", "cli:0.0", "#{pane_dead}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(value) == "0", nil
}

// Close is idempotent and reaps the exact owned foreground server. Cleanup has
// its own bounded context so a canceled request cannot strand a CLI daemon.
func (t *Terminal) Close(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return t.closeErr
	}
	var treeErr error
	if t.process != nil {
		// tmux resumes a stopped pane as part of job control. Freeze only our
		// owned server while stopping its pane tree, then allow it to reap.
		resume, pauseErr := pauseServer(t.server)
		treeErr = errors.Join(pauseErr, t.process.killTree())
		if resume != nil {
			resume()
		}
	}
	if t.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		select {
		case <-t.done:
		default:
			_, _ = t.run(ctx, nil, "kill-server")
			select {
			case <-t.done:
			case <-ctx.Done():
				_ = t.server.Process.Kill()
				select {
				case <-t.done:
				case <-time.After(time.Second):
					return errors.New("native terminal server reap timed out")
				}
			}
		}
	}
	if err := os.RemoveAll(t.dir); err != nil {
		return errors.Join(treeErr, err)
	}
	t.closed = true
	t.closeErr = treeErr
	return treeErr
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, errors.New("native terminal capture too large")
	}
	return b.Buffer.Write(p)
}
