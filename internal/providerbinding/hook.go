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
	"strconv"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

var providerIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{16,128}$`)

type hookPayload struct {
	SessionID      string `json:"session_id"`
	CWD            string `json:"cwd"`
	Event          string `json:"hook_event_name"`
	TranscriptPath string `json:"transcript_path"`
}

type HookResult struct {
	WakeFinal         bool
	NodeID            string
	SessionID         string
	ProviderSessionID string
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
	_, err := CaptureEvent(ctx, store, input, getenv, display, now, "codex")
	return err
}

func CaptureEvent(
	ctx context.Context,
	store *Store,
	input io.Reader,
	getenv func(string) string,
	display DisplayFunc,
	now func() time.Time,
	backend string,
) (HookResult, error) {
	if store == nil || input == nil || getenv == nil || display == nil || now == nil {
		return HookResult{}, errors.New("provider hook dependencies are required")
	}
	if backend != "codex" && backend != "claude" {
		return HookResult{}, errors.New("provider hook backend is invalid")
	}
	decoder := json.NewDecoder(io.LimitReader(input, 1<<20))
	var payload hookPayload
	if err := decoder.Decode(&payload); err != nil {
		return HookResult{}, fmt.Errorf("decode provider hook: %w", err)
	}
	if !supportedHookEvent(backend, payload.Event) {
		return HookResult{}, nil
	}
	if !providerIDPattern.MatchString(payload.SessionID) || !filepath.IsAbs(payload.CWD) ||
		!filepath.IsAbs(payload.TranscriptPath) {
		return HookResult{}, errors.New("provider hook payload is invalid")
	}
	nodeID := getenv(EnvNodeID)
	sessionID := getenv(EnvSessionID)
	tmuxSession := getenv(EnvTmuxSession)
	tmuxWindow := getenv(EnvTmuxWindow)
	pane := getenv("TMUX_PANE")
	if nodeID == "" || sessionID == "" || tmuxSession == "" || tmuxWindow == "" || pane == "" {
		return HookResult{}, nil // The hook belongs to a provider process not launched by Bria.
	}
	runtimeGeneration := uint64(0) // zero is the legacy pre-generation environment
	if generationText := strings.TrimSpace(getenv(EnvRuntimeGeneration)); generationText != "" {
		parsed, parseErr := strconv.ParseUint(generationText, 10, 64)
		if parseErr != nil {
			return HookResult{}, errors.New("provider runtime generation is invalid")
		}
		runtimeGeneration = parsed
	}
	displayed, err := display(ctx, pane)
	if err != nil {
		return HookResult{}, fmt.Errorf("inspect provider tmux pane: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(displayed), "\t")
	if len(parts) != 3 && len(parts) != 5 {
		return HookResult{}, errors.New("invalid provider tmux identity")
	}
	actualSession := parts[1]
	if actualSession == "" {
		actualSession = parts[0]
	}
	if actualSession != tmuxSession || parts[2] != tmuxWindow {
		return HookResult{}, errors.New("provider tmux identity does not match Bria launch")
	}
	windowID := ""
	if len(parts) == 5 {
		windowID = parts[3]
		if windowID == "" || parts[4] != pane {
			return HookResult{}, errors.New("provider tmux instance does not match Bria launch")
		}
	}
	workdir := filepath.Clean(payload.CWD)
	ref := domain.SessionRef{NodeID: domain.NodeID(nodeID), SessionID: domain.SessionID(sessionID)}
	if wakeFinalEvent(payload.Event) {
		if existing, found, lookupErr := store.LookupRef(ref); lookupErr != nil {
			return HookResult{}, lookupErr
		} else if found {
			if existing.ProviderSessionID != payload.SessionID ||
				existing.TmuxSession != tmuxSession || existing.TmuxWindow != tmuxWindow ||
				(existing.TmuxPane != "" && existing.TmuxPane != pane) {
				return HookResult{}, errors.New("provider stop does not match stored binding")
			}
			workdir = existing.Workdir
		}
	}
	if err := validateProviderTranscript(
		backend, payload.TranscriptPath, payload.SessionID, workdir,
	); err != nil {
		return HookResult{}, err
	}
	if err := store.Put(Record{
		NodeID: nodeID, SessionID: sessionID, ProviderSessionID: payload.SessionID,
		Workdir: workdir, TmuxSession: tmuxSession,
		TmuxWindow: tmuxWindow, TmuxWindowID: windowID, TmuxPane: pane,
		RuntimeGeneration: runtimeGeneration, UpdatedAt: now().UTC(),
	}); err != nil {
		return HookResult{}, err
	}
	return HookResult{
		WakeFinal: wakeFinalEvent(payload.Event), NodeID: nodeID,
		SessionID: sessionID, ProviderSessionID: payload.SessionID,
	}, nil
}

func supportedHookEvent(backend, event string) bool {
	switch event {
	case "SessionStart", "UserPromptSubmit", "Stop":
		return true
	case "StopFailure":
		return backend == "claude"
	default:
		return false
	}
}

func wakeFinalEvent(event string) bool { return event == "Stop" || event == "StopFailure" }

func validateProviderTranscript(backend, path, providerID, workdir string) error {
	if backend == "claude" {
		return validateClaudeTranscript(path, providerID, workdir)
	}
	return validateCodexTranscript(path, providerID, workdir)
}

func DisplayTmux(ctx context.Context, pane string) (string, error) {
	command := exec.CommandContext(ctx, "tmux", "display-message", "-t", pane, "-p",
		"#{session_name}\t#{session_group}\t#{window_name}\t#{window_id}\t#{pane_id}")
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

type claudeMeta struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

func validateClaudeTranscript(path, providerID, workdir string) error {
	file, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if filepath.Base(file) != providerID+".jsonl" {
		return errors.New("Claude transcript filename does not match hook payload")
	}
	handle, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("open provider transcript: %w", err)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Claude transcript must be a regular file")
	}
	scanner := bufio.NewScanner(io.LimitReader(handle, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for count := 0; count < 64 && scanner.Scan(); count++ {
		var meta claudeMeta
		if json.Unmarshal(scanner.Bytes(), &meta) != nil {
			continue
		}
		if meta.SessionID != "" && meta.SessionID != providerID {
			return errors.New("Claude transcript session does not match hook payload")
		}
		if meta.CWD != "" && filepath.IsAbs(meta.CWD) &&
			filepath.Clean(meta.CWD) != filepath.Clean(workdir) {
			return errors.New("Claude transcript workdir does not match Bria binding")
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read provider transcript: %w", err)
	}
	return nil
}
