package telegramapp

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// editNavigationScreen is the single navigation mutation point. Flood waits
// propagate to the poller so the callback is consumed without retrying it.
func (h *Handler) editNavigationScreen(
	ctx context.Context,
	origin telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	edited, err := h.messenger.EditScreen(ctx, origin, screen)
	// A Telegram flood wait applies to the chat. Sending a replacement here
	// only doubles the rejected traffic and extends the cooldown.
	return edited, err
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
