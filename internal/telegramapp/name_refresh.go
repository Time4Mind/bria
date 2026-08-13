package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
)

const nameRefreshLimit = 35 * time.Second

// scheduleNameRefresh updates the already-sent card once the origin node's
// lightweight naming call has committed. It never blocks Telegram polling.
func (h *Handler) scheduleNameRefresh(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	message telegrambot.Message,
) {
	h.paneMu.Lock()
	generation := h.paneGeneration[actor.UserID]
	h.paneMu.Unlock()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		timer := time.NewTimer(nameRefreshLimit)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				return
			case <-ticker.C:
				if !h.canRefresh() || !h.currentPaneGeneration(actor.UserID, generation) {
					return
				}
				session, err := h.service.Session(actor, ref)
				if err != nil || !session.IsLive() {
					return
				}
				if session.Name == "" {
					continue
				}
				screen, err := h.renderSessionCard(ctx, actor, ref, 0)
				if err == nil {
					_, _ = h.messenger.EditScreen(ctx, message, screen)
				}
				return
			}
		}
	}()
}
