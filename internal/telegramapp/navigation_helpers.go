package telegramapp

import (
	"strings"

	"github.com/Time4Mind/bria/internal/telegramui"
)

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
