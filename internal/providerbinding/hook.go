package providerbinding

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var providerIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{16,128}$`)

type hookPayload struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
	TranscriptPath string `json:"transcript_path"`
}

type codexMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"payload"`
}

type DisplayFunc func(context.Context, string) (string, error)

func Capture(
	ctx context.Context,
	store *Store,
	input io.Reader,
	getenv func(string) string,
	display DisplayFunc,
	now func() time.Time,
) error {
	if store == nil || input == nil || getenv == nil || display == nil || now == nil {
		return errors.New("provider hook dependencies are required")
	}
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	var payload hookPayload
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode provider hook: %w", err)
	}
	if payload.Event != "SessionStart" && payload.Event != "UserPromptSubmit" {
		return nil
	}
	if !providerIDPattern.MatchString(payload.SessionID) || !filepath.IsAbs(payload.CWD) ||
		!filepath.IsAbs(payload.TranscriptPath) {
		return errors.New("provider hook payload is invalid")
	}
	nodeID := getenv(EnvNodeID)
	sessionID := getenv(EnvSessionID)
	tmuxSession := getenv(EnvTmuxSession)
	tmuxWindow := getenv(EnvTmuxWindow)
	pane := getenv("TMUX_PANE")
	if nodeID == "" || sessionID == "" || tmuxSession == "" || tmuxWindow == "" || pane == "" {
		return nil // The hook belongs to a provider process not launched by Bria.
	}
	displayed, err := display(ctx, pane)
	if err != nil {
		return fmt.Errorf("inspect provider tmux pane: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(displayed), "\t")
	if len(parts) != 3 {
		return errors.New("invalid provider tmux identity")
	}
	actualSession := parts[1]
	if actualSession == "" {
		actualSession = parts[0]
	}
	if actualSession != tmuxSession || parts[2] != tmuxWindow {
		return errors.New("provider tmux identity does not match Bria launch")
	}
	if err := validateCodexTranscript(payload.TranscriptPath, payload.SessionID, payload.CWD); err != nil {
		return err
	}
	return store.Put(Record{
		NodeID: nodeID, SessionID: sessionID, ProviderSessionID: payload.SessionID,
		Workdir: filepath.Clean(payload.CWD), TmuxSession: tmuxSession,
		TmuxWindow: tmuxWindow, UpdatedAt: now().UTC(),
	})
}

func DisplayTmux(ctx context.Context, pane string) (string, error) {
	command := exec.CommandContext(ctx, "tmux", "display-message", "-t", pane, "-p",
		"#{session_name}\t#{session_group}\t#{window_name}")
	output, err := command.Output()
	return string(output), err
}

func validateCodexTranscript(path, providerID, workdir string) error {
	file, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	handle, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("open provider transcript: %w", err)
	}
	defer handle.Close()
	scanner := bufio.NewScanner(io.LimitReader(handle, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for count := 0; count < 16 && scanner.Scan(); count++ {
		var meta codexMeta
		if json.Unmarshal(scanner.Bytes(), &meta) != nil || meta.Type != "session_meta" {
			continue
		}
		if meta.Payload.ID != providerID || filepath.Clean(meta.Payload.CWD) != filepath.Clean(workdir) {
			return errors.New("provider transcript metadata does not match hook payload")
		}
		return nil
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read provider transcript: %w", err)
	}
	return errors.New("provider transcript metadata is missing")
}
