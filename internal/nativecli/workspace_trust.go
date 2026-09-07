package nativecli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Only the exact selected workspace and known affirmative folder-trust menu
// are eligible. Server/gateway/settings trust and generic permission prompts
// never match. Key selection is anchored to visible selected options.
func workspaceTrustKeys(screen, workdir string) ([]string, bool) {
	if !filepath.IsAbs(workdir) {
		return nil, false
	}
	path, yes, no, selected := false, false, false, 0
	selectedCount := 0
	unnumbered, noSeenBeforeYes := false, false
	for _, raw := range strings.Split(screen, "\n") {
		line := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "│┃"))
		if line == workdir || line == "Workspace: "+workdir || line == "Directory: "+workdir {
			path = true
		}
		if filepath.IsAbs(line) && line != workdir {
			return nil, false
		}
		marked := strings.HasPrefix(line, "❯") || strings.HasPrefix(line, "›") || strings.HasPrefix(line, ">")
		if marked {
			selectedCount++
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "❯›>"))
		switch line {
		case "No, exit", "No, continue without these permissions":
			if yes {
				return nil, false
			}
			no = true
			unnumbered = true
			if marked {
				selected = 2
			}
		case "Yes, I trust this folder":
			yes = true
			unnumbered = true
			noSeenBeforeYes = no
			if marked {
				selected = 1
			}
		case "1. Yes, I trust this folder":
			yes = true
			if marked {
				selected = 1
			}
		case "2. No, exit", "2. No, continue without these permissions":
			no = true
			if marked {
				selected = 2
			}
		}
	}
	// Claude2.1.251's folder-specific Confirm is unnumbered/cancel-first.
	// Require the visible cursor, both exact labels/order, and selected workdir.
	if unnumbered && path && yes && no && noSeenBeforeYes && selectedCount == 1 && selected != 0 {
		if selected == 1 {
			return []string{"Enter"}, true
		}
		return []string{"Down", "Enter"}, true
	}
	if !path || !yes || !no || selected == 0 || selectedCount != 1 {
		return nil, false
	}
	if selected == 2 {
		return []string{"Up", "Enter"}, true
	}
	return []string{"Enter"}, true
}

func applyWorkspaceTrust(ctx context.Context, terminal Terminal, keys []string, workdir string) error {
	if len(keys) == 2 {
		if err := terminal.Key(ctx, keys[0]); err != nil {
			return err
		}
		confirmed := false
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			screen, err := terminal.Capture(ctx)
			if err != nil {
				return err
			}
			next, ok := workspaceTrustKeys(screen, workdir)
			if ok && len(next) == 1 && next[0] == "Enter" {
				confirmed = true
				break
			}
		}
		if !confirmed {
			return errors.New("native CLI workspace trust confirmation required")
		}
	}
	return terminal.Key(ctx, "Enter")
}
