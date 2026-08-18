package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// editNavigationScreen keeps explicit user navigation responsive when
// Telegram temporarily rejects edits. Sends are a separate Bot API budget in
// practice, so replace the carrier once instead of retrying the same edit and
// blocking every newer update behind it.
func (h *Handler) editNavigationScreen(
	ctx context.Context,
	origin telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	edited, err := h.messenger.EditScreen(ctx, origin, screen)
	retryAfter, limited := telegrambot.FloodWait(err)
	if !limited {
		return edited, err
	}
	log.Printf("bria telegram: navigation_edit_limited retry_after_ms=%d fallback=send",
		retryAfter.Milliseconds())
	replacement, sendErr := h.messenger.SendScreen(ctx, origin.ChatID, screen)
	if sendErr != nil {
		return telegrambot.Message{}, fmt.Errorf(
			"replace rate-limited navigation card: %w", errors.Join(err, sendErr),
		)
	}
	// The replacement is already visible and usable. Removing the stale carrier
	// is best effort because Telegram may rate-limit that method independently.
	_ = h.messenger.DeleteMessage(ctx, origin)
	return replacement, nil
}

func leavesSessionCard(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionStop, telegramui.ActionTerminal, telegramui.ActionConfirmClose,
		telegramui.ActionConfirmClear, telegramui.ActionCancelControl,
		telegramui.ActionKeyUp, telegramui.ActionKeyDown, telegramui.ActionKeyLeft,
		telegramui.ActionKeyRight, telegramui.ActionKeyEnter, telegramui.ActionKeyEscape,
		telegramui.ActionKeySpace, telegramui.ActionKeyTab, telegramui.ActionKeyCtrlC,
		telegramui.ActionKeyBack:
		return false
	default:
		return true
	}
}

func isLegacyNodeSessionsScreen(text string) bool {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.Contains(first, " · ") &&
		(strings.Contains(first, "сесси") || strings.Contains(first, "session") ||
			strings.Contains(first, "会话"))
}
