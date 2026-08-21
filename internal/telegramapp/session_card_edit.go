package telegramapp

import (
	"context"
	"time"

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
	queuedAt := time.Now()
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	if _, restoreTagged := restoreTimingFromContext(ctx); restoreTagged {
		logSlowTelegramOperationContext(
			ctx, "card_edit_queue", origin.MessageID, queuedAt, nil,
		)
	}
	return h.editCardTransportLocked(ctx, origin, screen)
}
