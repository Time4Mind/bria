package telegramapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) canRefresh() bool {
	return h.leadership == nil || h.leadership.IsLeader()
}

// RunInteractiveNotifications observes replicated prompt metadata on every
// node. Followers remember it too, limiting duplicate alerts after failover;
// only the current leader communicates with Telegram.
func (h *Handler) RunInteractiveNotifications(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	seen := make(map[string]string)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		h.scanInteractiveNotifications(ctx, seen)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) scanInteractiveNotifications(ctx context.Context, seen map[string]string) {
	for _, notice := range h.service.InteractiveNotices() {
		if !notice.Active {
			continue
		}
		prompt := notice.Session.InteractivePrompt
		key := fmt.Sprintf("%d\x00%s", notice.UserID, notice.Session.Ref().Key())
		if prompt == nil || seen[key] == prompt.Hash {
			continue
		}
		if !h.canRefresh() {
			seen[key] = prompt.Hash
			continue
		}
		if h.refreshActiveInteractive(ctx, notice) {
			seen[key] = prompt.Hash
		}
	}
}

func (h *Handler) refreshActiveInteractive(
	ctx context.Context,
	notice application.InteractiveNotice,
) bool {
	actor := application.Principal{UserID: notice.UserID}
	screen, interactive, err := h.renderInteractiveSessionCard(
		ctx, actor, notice.Session.Ref(),
	)
	if err != nil || !interactive {
		// The replicated prompt can arrive just before node-control can return
		// its pane. Leave the notice unseen so the next scan retries instead of
		// permanently replacing the keyboard with a regular card.
		return false
	}
	return h.repostInteractiveResponseCard(ctx, actor, notice, screen)
}

func (h *Handler) repostInteractiveResponseCard(
	ctx context.Context,
	actor application.Principal,
	notice application.InteractiveNotice,
	screen telegramui.Screen,
) bool {
	ref := notice.Session.Ref()
	if notice.Session.InteractivePrompt == nil {
		return false
	}
	promptHash := notice.Session.InteractivePrompt.Hash
	fingerprint := telegrambot.ScreenFingerprint(screen)
	h.cancelPaneRefresh(actor.UserID)
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	h.cardMutationMu.Lock()
	defer h.cardMutationMu.Unlock()

	current, exists, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return false
	}
	if exists && current.Session == ref && current.ScreenHash == fingerprint {
		return true
	}
	replacement, err := h.messenger.SendScreen(ctx, int64(actor.UserID), screen)
	if err != nil {
		return false
	}
	latest, latestErr := h.service.ActiveSession(actor)
	if latestErr != nil {
		_ = h.messenger.DeleteMessage(ctx, replacement)
		return false
	}
	if latest.Ref() != ref || latest.InteractivePrompt == nil ||
		latest.InteractivePrompt.Hash != promptHash {
		_ = h.messenger.DeleteMessage(ctx, replacement)
		return true
	}
	h.recordResponseCard(ctx, actor, replacement, screen)
	committed, ok, commitErr := h.service.TelegramResponseCard(actor)
	if commitErr != nil || !ok || committed.ChatID != replacement.ChatID ||
		committed.MessageID != replacement.MessageID || committed.ScreenHash != fingerprint {
		_ = h.messenger.DeleteMessage(ctx, replacement)
		return false
	}
	if exists {
		// Preserve the exact screen the user was reading as history, but remove
		// stale controls so only the new waiting-input carrier can drive the CLI.
		h.freezeHistoricalCard(ctx, actor, telegramMessage(current), ref)
	}
	return true
}

func telegramMessage(card domain.TelegramResponseCard) telegrambot.Message {
	paneHash := card.PaneHash
	if _, decodedPaneHash, ok := decodeSessionPagePaneHash(card.PaneHash); ok {
		paneHash = decodedPaneHash
	}
	return telegrambot.Message{
		ChatID: card.ChatID, MessageID: card.MessageID, Rich: card.Rich,
		RichMediaFileID: card.RichMediaFileID, PaneHash: paneHash,
		ScreenHash: card.ScreenHash,
	}
}
