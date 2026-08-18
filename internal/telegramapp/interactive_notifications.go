package telegramapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
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
		delivered := false
		if notice.Active {
			delivered = h.refreshActiveInteractive(ctx, notice)
		} else {
			delivered = h.sendInteractiveNotification(ctx, notice)
		}
		if delivered {
			seen[key] = prompt.Hash
		}
	}
}

func (h *Handler) sendInteractiveNotification(
	ctx context.Context,
	notice application.InteractiveNotice,
) bool {
	actor := application.Principal{UserID: notice.UserID}
	token, err := h.tokens.Session(
		notice.UserID, telegramui.ActionSelectSession, notice.Session.Ref(),
	)
	if err != nil {
		return false
	}
	copy := h.copy(actor)
	label := notice.Session.Name
	if label == "" {
		label = "…"
	}
	screen := telegramui.Screen{
		Name: telegramui.ScreenSessionCard,
		Text: fmt.Sprintf("❗ %s · %s\n%s", label, notice.Node.Name,
			copy.Text(i18n.SessionPhaseWaiting)),
		Grid: telegramui.Grid{telegramui.Row{{
			Label:    label,
			Callback: telegramui.Callback{Action: telegramui.ActionSelectSession, Token: token},
		}}},
	}
	_, err = h.messenger.SendScreen(ctx, int64(notice.UserID), screen)
	return err == nil
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
	card, exists, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return false
	}
	if !exists {
		message, sendErr := h.messenger.SendScreen(ctx, int64(notice.UserID), screen)
		if sendErr != nil {
			return false
		}
		h.rememberResponseCard(ctx, actor, message, screen)
		return true
	}
	if card.Session != notice.Session.Ref() {
		// The session is selected but the user explicitly navigated elsewhere.
		// Notify in a separate message instead of replacing that screen.
		return h.sendInteractiveNotification(ctx, notice)
	}
	_, err = h.editResponseCard(ctx, actor, telegramMessage(card), screen)
	return err == nil
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
