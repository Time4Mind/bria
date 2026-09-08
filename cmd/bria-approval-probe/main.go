// bria-approval-probe is an opt-in local diagnostic, not the production observer.
// Inspect first, independently review the command, then authorize its exact hash.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bria/internal/nativeapproval"
)

type terminal struct {
	target, identity string
	execute          func(context.Context, ...string) (string, error)
}

func (t *terminal) run(ctx context.Context, args ...string) (string, error) {
	if t.execute != nil {
		return t.execute(ctx, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return "", errors.New("tmux operation failed")
	}
	return string(out), nil
}
func (t *terminal) check(ctx context.Context) error {
	id, err := t.run(ctx, "display-message", "-p", "-t", t.target, "#{session_id}:#{window_id}:#{pane_id}:#{pane_pid}:#{pane_dead}")
	if err != nil || strings.TrimSpace(id) != t.identity {
		return errors.New("pane identity changed")
	}
	return nil
}
func (t *terminal) Capture(ctx context.Context) (string, error) {
	if err := t.check(ctx); err != nil {
		return "", err
	}
	return t.run(ctx, "capture-pane", "-p", "-t", t.target)
}
func (t *terminal) Input(context.Context, string) error {
	return errors.New("prompt input unsupported")
}
func (t *terminal) Key(ctx context.Context, key string) error {
	if key != "Enter" {
		return errors.New("only one-shot Enter supported")
	}
	if err := t.check(ctx); err != nil {
		return err
	}
	_, err := t.run(ctx, "send-keys", "-t", t.target, "Enter")
	return err
}

func (t *terminal) Expand(ctx context.Context, rows int) error {
	if rows < 40 || rows > 256 {
		return errors.New("unsupported terminal height")
	}
	if err := t.check(ctx); err != nil {
		return err
	}
	value, err := t.run(ctx, "display-message", "-p", "-t", t.target, "#{window_height}")
	if err != nil {
		return err
	}
	current, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	if current >= rows {
		return nil
	}
	parts := strings.Split(t.identity, ":")
	if len(parts) != 5 {
		return errors.New("invalid window identity")
	}
	_, err = t.run(ctx, "resize-window", "-t", parts[1], "-y", strconv.Itoa(rows))
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	target := flag.String("target", "", "exact tmux pane ID, such as %12")
	identity := flag.String("identity", "", "expected session:window:pane:pid:dead tuple from inspect")
	fingerprint := flag.String("accept-once", "", "independently authorized exact menu SHA256")
	stateDir := flag.String("state-dir", ".cache/approval-probe", "durable one-shot claims; reuse for every invocation")
	expand := flag.Bool("expand", false, "recover collapsed approval by growing the owned window to at most 256 rows")
	flag.Parse()
	if !regexp.MustCompile(`^%[0-9]+$`).MatchString(*target) {
		return errors.New("exact pane ID required")
	}
	ctx := context.Background()
	t := &terminal{target: *target, identity: *identity}
	if *fingerprint != "" {
		if *identity == "" {
			return errors.New("expected pane identity required")
		}
		if err := claimOnce(*stateDir, *identity, *fingerprint); err != nil {
			return err
		}
		if err := nativeapproval.AcceptCodexCommandOnce(ctx, t, *fingerprint); err != nil {
			return err
		}
		fmt.Println(`{"decision":"accept-once","approval_receipt":true,"command_success":"verify tool result"}`)
		return nil
	}
	id, err := t.run(ctx, "display-message", "-p", "-t", t.target, "#{session_id}:#{window_id}:#{pane_id}:#{pane_pid}:#{pane_dead}")
	if err != nil {
		return err
	}
	t.identity = strings.TrimSpace(id)
	if !strings.HasSuffix(t.identity, ":0") {
		return errors.New("pane is not live")
	}
	var screen string
	if *expand {
		screen, err = nativeapproval.CaptureComplete(ctx, t)
	} else {
		screen, err = t.Capture(ctx)
	}
	if err != nil {
		return err
	}
	request, ok := nativeapproval.ParseCodexCommandApproval(screen)
	return json.NewEncoder(os.Stdout).Encode(struct {
		Identity   string                              `json:"identity"`
		Recognized bool                                `json:"recognized"`
		Request    nativeapproval.CodexCommandApproval `json:"request"`
	}{t.identity, ok, request})
}

// Claims survive failed/unknown outcomes and process restarts. Never automatically
// clear one to retry: the earlier Enter may already have reached the CLI.
func claimOnce(dir, identity, fingerprint string) error {
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fingerprint) {
		return errors.New("invalid fingerprint")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(identity + ":" + fingerprint))
	f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%x.claim", sum)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("approval already claimed or state unavailable; do not resend")
	}
	return f.Close()
}
