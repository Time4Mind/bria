package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramoutbound"
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
	release, err := h.responseCards.acquire(ctx, actor.UserID)
	if err != nil {
		return telegrambot.Message{}, err
	}
	defer release()
	if _, restoreTagged := restoreTimingFromContext(ctx); restoreTagged {
		telegramoutbound.LogOperation(
			ctx, "card_edit_queue", origin.MessageID, queuedAt, nil,
		)
	}
	return h.editCardTransportCoordinated(ctx, origin, screen)
}
