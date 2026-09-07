package claudequota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"bria/internal/domain"
	"bria/internal/processgroup"
	"bria/internal/telegramstatus"
)

const (
	quotaWindow   = "__bria_quota_claude__"
	maxOutputSize = 1 << 20
)

type Spec struct {
	Executable  string
	Arguments   []string
	Environment []string
}

type Collector struct {
	nodeID        domain.ComputerID
	spec          Spec
	socket        string
	tmuxPath      string
	serverCreated bool
	warmed        bool
	kimi          *kimiClient
}

func New(nodeID domain.ComputerID, spec Spec) *Collector {
	digest := sha256.Sum256([]byte(nodeID))
	spec.Arguments = append([]string(nil), spec.Arguments...)
	spec.Environment = append([]string(nil), spec.Environment...)
	return &Collector{nodeID: nodeID, spec: spec,
		kimi:   newKimiClient(spec.Environment),
		socket: "bria-quota-" + strconv.Itoa(os.Getpid()) + "-" + fmt.Sprintf("%x", digest[:5])}
}

func (collector *Collector) Collect(ctx context.Context) (telegramstatus.Snapshot, error) {
	if collector == nil || collector.spec.Executable == "" || collector.spec.Environment == nil {
		return telegramstatus.Snapshot{}, errors.New("Claude quota collector is unavailable")
	}
	if collector.kimi != nil {
		if snapshot, configured, err := collector.kimi.Collect(ctx, collector.nodeID); configured {
			if err == nil {
				return snapshot, nil
			}
			// A configured Kimi endpoint is authoritative; do not open a
			// second interactive Claude session when its usage API fails.
			return telegramstatus.Snapshot{}, err
		}
	}
	if collector.tmuxPath == "" {
		path, err := exec.LookPath("tmux")
		if err != nil {
			return telegramstatus.Snapshot{}, err
		}
		collector.tmuxPath = path
	}
	if err := collector.ensureWindow(ctx); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	snapshot, err := collector.poll(ctx)
	if err == nil {
		return snapshot, nil
	}
	collector.Close(ctx)
	collector.warmed = false
	if err := collector.ensureWindow(ctx); err != nil {
		return telegramstatus.Snapshot{}, err
	}
	return collector.poll(ctx)
}

func (collector *Collector) Close(ctx context.Context) {
	if collector == nil || !collector.serverCreated || collector.tmuxPath == "" {
		return
	}
	_, _ = collector.tmux(ctx, "kill-server")
	collector.serverCreated = false
}

func (collector *Collector) ensureWindow(ctx context.Context) error {
	if collector.serverCreated {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	args := []string{"new-session", "-d", "-s", "quota", "-n", quotaWindow, "-c", home, collector.spec.Executable}
	args = append(args, collector.spec.Arguments...)
	result, err := collector.tmux(ctx, args...)
	if err != nil || result.exitCode != 0 {
		return errors.New("create isolated Claude quota session")
	}
	collector.serverCreated = true
	if err := collector.waitForPrompt(ctx); err != nil {
		return err
	}
	if !collector.warmed {
		if err := collector.warm(ctx); err != nil {
			return err
		}
		collector.warmed = true
	}
	return nil
}

func (collector *Collector) waitForPrompt(ctx context.Context) error {
	for attempt := 0; attempt < 16; attempt++ {
		pane, err := collector.capture(ctx)
		if err == nil {
			lower := strings.ToLower(pane)
			if strings.Contains(lower, "trust this folder") || strings.Contains(lower, "is this a project you") {
				// Claude's current trust screen selects "No, exit" by default.
				// Move to the affirmative row before confirming; pressing Enter
				// directly exits and leaves usage permanently unavailable.
				_, _ = collector.tmux(ctx, "send-keys", "-t", target(), "Down")
				_, _ = collector.tmux(ctx, "send-keys", "-t", target(), "C-m")
			} else if strings.Contains(pane, "? for shortcuts") {
				return nil
			}
		}
		if err := wait(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
	return errors.New("Claude quota session did not reach its prompt")
}

func (collector *Collector) warm(ctx context.Context) error {
	result, err := collector.tmux(ctx, "send-keys", "-t", target(), "-l", "respond with exactly: ok")
	if err != nil || result.exitCode != 0 {
		return errors.New("warm Claude quota session")
	}
	_, _ = collector.tmux(ctx, "send-keys", "-t", target(), "C-m")
	for attempt := 0; attempt < 24; attempt++ {
		if err := wait(ctx, 500*time.Millisecond); err != nil {
			return err
		}
		pane, captureErr := collector.capture(ctx)
		if captureErr == nil && !strings.Contains(pane, "esc to interrupt") {
			return nil
		}
	}
	return errors.New("Claude quota warm-up did not finish")
}

func (collector *Collector) poll(ctx context.Context) (telegramstatus.Snapshot, error) {
	_, _ = collector.tmux(ctx, "send-keys", "-t", target(), "Escape")
	_, _ = collector.tmux(ctx, "clear-history", "-t", target())
	result, err := collector.tmux(ctx, "send-keys", "-t", target(), "-l", "/usage")
	if err != nil || result.exitCode != 0 {
		return telegramstatus.Snapshot{}, errors.New("send Claude usage command")
	}
	_, _ = collector.tmux(ctx, "send-keys", "-t", target(), "C-m")
	last := ""
	for attempt := 0; attempt < 60; attempt++ {
		if err := wait(ctx, 200*time.Millisecond); err != nil {
			return telegramstatus.Snapshot{}, err
		}
		pane, captureErr := collector.capture(ctx)
		if captureErr != nil {
			continue
		}
		snapshot, ok := Parse(pane, collector.nodeID, time.Now())
		if !ok {
			continue
		}
		fingerprint := fmt.Sprintf("%v:%v", snapshot.FiveHour, snapshot.Weekly)
		if fingerprint == last {
			_, _ = collector.tmux(ctx, "send-keys", "-t", target(), "Escape")
			return snapshot, nil
		}
		last = fingerprint
	}
	return telegramstatus.Snapshot{}, errors.New("Claude usage view did not settle")
}

func (collector *Collector) capture(ctx context.Context) (string, error) {
	result, err := collector.tmux(ctx, "capture-pane", "-p", "-S", "-100", "-t", target())
	if err != nil || result.exitCode != 0 {
		return "", errors.New("capture Claude quota session")
	}
	return string(result.output), nil
}

type tmuxResult struct {
	output   []byte
	exitCode int
}

func (collector *Collector) tmux(ctx context.Context, arguments ...string) (tmuxResult, error) {
	args := append([]string{"-L", collector.socket}, arguments...)
	command := exec.CommandContext(ctx, collector.tmuxPath, args...)
	command.Env = append([]string(nil), collector.spec.Environment...)
	if err := processgroup.Configure(command); err != nil {
		return tmuxResult{}, err
	}
	command.Cancel = func() error { return processgroup.KillTree(command) }
	output := &limitedBuffer{limit: maxOutputSize}
	command.Stdout, command.Stderr = output, io.Discard
	err := command.Run()
	result := tmuxResult{output: append([]byte(nil), output.Bytes()...)}
	if command.ProcessState != nil {
		result.exitCode = command.ProcessState.ExitCode()
	}
	if output.exceeded {
		return result, errors.New("tmux response exceeds limit")
	}
	if err != nil && ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, err
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	allowed := buffer.limit - buffer.Len()
	if allowed <= 0 {
		buffer.exceeded = true
		return len(data), nil
	}
	if len(data) > allowed {
		buffer.exceeded = true
		_, _ = buffer.Buffer.Write(data[:allowed])
		return len(data), nil
	}
	return buffer.Buffer.Write(data)
}

func target() string { return "quota:" + quotaWindow }

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
