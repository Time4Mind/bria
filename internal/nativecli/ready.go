package nativecli

import (
	"bria/internal/domain"
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Terminal interface {
	Capture(context.Context) (string, error)
	Input(context.Context, string) error
	Key(context.Context, string) error
}

type State struct {
	SessionID, Model   string
	Content            string
	Ready, Interactive bool
}

var sessionLine = regexp.MustCompile(`(?im)\bsession(?:\s+id)?\s*:\s*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b`)

func ParseScreen(text string) State {
	state := State{}
	if m := sessionLine.FindStringSubmatch(text); len(m) > 1 {
		state.SessionID = strings.ToLower(m[1])
	}
	state.Model = screenModel(text)
	state.Content, state.Interactive = interactiveContent(text)
	lines := strings.Split(text, "\n")
	composer := -1
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "›") || strings.HasPrefix(line, "❯") || line == ">" {
			state.Ready = true
			composer = index
		}
	}
	// Resume can leave historical prompt markers visible above the live startup
	// footer. Those are not evidence that Enter can currently submit input.
	if composer >= 0 {
		state.Ready = idleComposer(lines[composer:])
		footer := strings.ToLower(strings.Join(lines[composer:], "\n"))
		for _, marker := range []string{"starting mcp servers", "esc to interrupt", "escape to interrupt", "tab to queue message"} {
			if strings.Contains(footer, marker) {
				state.Ready = false
			}
		}
	}
	if state.Interactive {
		state.Ready = false
	}
	return state
}

// A historical prompt anywhere on screen is not a live composer. The last
// prompt must be at the bottom, followed only by native footer/border rows.
// Nonempty slash drafts are never a safe startup injection point.
func idleComposer(lines []string) bool {
	draft := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(lines[0]), "›❯>"))
	if strings.HasPrefix(draft, "/") {
		return false
	}
	nonempty, footer := 0, false
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonempty++
		if nonempty > 4 {
			return false
		}
		if strings.Trim(line, "─━═│└┘╰╯┌┐╭╮+- ") == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "? for shortcuts") || strings.Contains(lower, "% context left") ||
			strings.Contains(lower, "shift+tab to cycle") || strings.Contains(lower, "shift+tab for") ||
			((strings.HasPrefix(lower, "gpt-") || strings.HasPrefix(lower, "codex-")) && strings.Contains(line, " · ")) {
			footer = true
			continue
		}
		return false
	}
	return draft == "" || footer
}

// Ready never submits a user prompt. Codex identity comes from the exact pane's
// native /status, not a directory-wide newest-session heuristic.
func Ready(ctx context.Context, terminal Terminal, plan Plan) (State, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if terminal == nil {
		return State{}, errors.New("native CLI terminal is required")
	}
	if plan.Provider != domain.ProviderCodex && plan.Provider != domain.ProviderClaude {
		return State{}, errors.New("unsupported native CLI provider")
	}
	statusSent := false
	var statusSentAt time.Time
	statusRetried := false
	trustAttempts := 0
	var trustSentAt time.Time
	for {
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		text, err := terminal.Capture(ctx)
		if err != nil {
			return State{}, err
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "--dangerously-skip-permissions cannot be used with root/sudo privileges") {
			return State{}, errors.New("native CLI bypass forbidden for current operating user")
		}
		if strings.Contains(lower, "sign in with") || strings.Contains(lower, "please log in") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "login required") {
			return State{}, errors.New("native CLI authentication required")
		}
		if strings.Contains(lower, "do you trust") || strings.Contains(lower, "trust this folder") || strings.Contains(lower, "trust the files") {
			if plan.Provider == domain.ProviderClaude {
				keys, exact := workspaceTrustKeys(text, plan.Workdir)
				if exact {
					// A native rerender or repeated startup confirmation can leave
					// this exact menu open after Enter. Revalidate on every attempt;
					// allow rendering to settle and never send an unbounded key loop.
					if trustAttempts == 0 || time.Since(trustSentAt) >= 750*time.Millisecond {
						if trustAttempts >= 3 {
							return State{}, errors.New("native CLI workspace trust confirmation required")
						}
						if err := applyWorkspaceTrust(ctx, terminal, keys, plan.Workdir); err != nil {
							return State{}, err
						}
						trustAttempts++
						trustSentAt = time.Now()
					}
					select {
					case <-ctx.Done():
						return State{}, ctx.Err()
					case <-time.After(50 * time.Millisecond):
					}
					continue
				}
			}
			return State{}, errors.New("native CLI workspace trust confirmation required")
		}
		state := ParseScreen(text)
		if plan.Provider == domain.ProviderClaude && state.Ready {
			state.SessionID = plan.SessionID
			if !uuidPattern.MatchString(state.SessionID) {
				return State{}, errors.New("native Claude requires declared session UUID")
			}
			return state, nil
		}
		if plan.Provider == domain.ProviderCodex {
			if statusSent && state.SessionID != "" {
				if plan.SessionID != "" && !strings.EqualFold(state.SessionID, plan.SessionID) {
					return State{}, errors.New("native Codex resumed a different session")
				}
				if err := terminal.Key(ctx, "Escape"); err != nil {
					return State{}, err
				}
				state.Ready = true
				return state, nil
			}
			// Startup may accept editing before submit is enabled. Only our exact
			// read-only /status draft can receive one delayed Enter, never a repaste
			// or generic prompt retry. The original handshake deadline is unchanged.
			if statusSent && !statusRetried && time.Since(statusSentAt) >= time.Second && statusDraftRetryEligible(text) {
				statusRetried = true
				if err := terminal.Key(ctx, "Enter"); err != nil {
					return State{}, err
				}
			}
			if state.Ready && !statusSent {
				if err := terminal.Input(ctx, "/status"); err != nil {
					return State{}, err
				}
				statusSent = true
				statusSentAt = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
