package nativeapproval

import (
	"context"
	"strings"
	"time"
)

// Expand only grows the owned terminal canvas; it never sends a decision key.
type expandableTerminal interface {
	Expand(context.Context, int) error
}

func NeedsExpansion(screen string) bool {
	return (strings.Contains(screen, "Would you like to run the following command?") ||
		strings.Contains(screen, "Would you like to make the following edits?")) &&
		strings.Contains(screen, "ctrl + a view all") && strings.Contains(screen, "Press enter to confirm or esc to cancel")
}

// CaptureComplete recovers a CLI-collapsed approval by growing the owned canvas.
// Scrollback cannot recover content the CLI did not render. After three bounded
// expansions a still-collapsed request remains non-actionable; no key is sent.
func CaptureComplete(ctx context.Context, terminal Terminal) (string, error) {
	screen, err := terminal.Capture(ctx)
	if err != nil || !NeedsExpansion(screen) {
		return screen, err
	}
	expander, ok := terminal.(expandableTerminal)
	if !ok {
		return screen, nil
	}
	for _, rows := range []int{80, 160, 256} {
		if err := expander.Expand(ctx, rows); err != nil {
			return "", err
		}
		for frame := 0; frame < 5; frame++ {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			next, err := terminal.Capture(ctx)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(next) == "" {
				continue
			}
			screen = next
			if !NeedsExpansion(screen) {
				return screen, nil
			}
		}
	}
	return screen, nil
}
