package telegramapp

import (
	"context"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// editExplicitSessionScreen makes a user-driven session-card mutation the
// final writer relative to the live pane worker. Rendering and control work
// stay outside the lock, so the callback waits only for an edit already in
// flight and for its own Telegram request.
func (h *Handler) editExplicitSessionScreen(
	ctx context.Context,
	actor application.Principal,
	origin telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	h.cancelPaneRefresh(actor.UserID)
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	return h.messenger.EditScreen(ctx, origin, screen)
}
