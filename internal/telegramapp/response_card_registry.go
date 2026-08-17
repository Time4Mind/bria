package telegramapp

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) rememberResponseCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	screen telegramui.Screen,
) {
	h.cardMutationMu.Lock()
	defer h.cardMutationMu.Unlock()
	h.rememberResponseCardLocked(ctx, actor, message, screen)
}

func (h *Handler) resolveCallbackCarrier(
	actor application.Principal,
	origin telegrambot.Message,
) telegrambot.Message {
	card, ok, err := h.service.TelegramResponseCard(actor)
	if err != nil || !ok || card.ChatID != origin.ChatID || card.MessageID != origin.MessageID {
		return origin
	}
	resolved := telegramMessage(card)
	resolved.Text = origin.Text
	return resolved
}

func (h *Handler) rememberResponseCardLocked(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	screen telegramui.Screen,
) {
	previous, exists, changed := h.recordResponseCard(ctx, actor, message, screen)
	if !changed {
		return
	}
	preferences, err := h.service.Preferences(actor)
	if err != nil || !exists {
		return
	}
	if previous.ChatID == message.ChatID && previous.MessageID == message.MessageID {
		return
	}
	previousMessage := telegrambot.Message{ChatID: previous.ChatID, MessageID: previous.MessageID}
	if preferences.EffectiveResponseCards() == domain.ResponseCardsReplace {
		_ = h.messenger.DeleteMessage(ctx, previousMessage)
		return
	}
	_ = h.messenger.ClearKeyboard(ctx, previousMessage)
}

func (h *Handler) recordResponseCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	screen telegramui.Screen,
) (domain.TelegramResponseCard, bool, bool) {
	previous, exists, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return domain.TelegramResponseCard{}, false, false
	}
	card := domain.TelegramResponseCard{
		ChatID: message.ChatID, MessageID: message.MessageID, Rich: message.Rich,
		RichMediaFileID: message.RichMediaFileID, PaneHash: message.PaneHash,
	}
	if checkpoint := screen.Checkpoint; checkpoint != nil {
		card.Session = domain.SessionRef{
			NodeID: domain.NodeID(checkpoint.NodeID), SessionID: domain.SessionID(checkpoint.SessionID),
		}
		card.SessionRevision = checkpoint.Revision
		card.SessionEventAt = checkpoint.EventAt
		card.RenderedFinalAt = checkpoint.RenderedFinalAt
	}
	if exists && previous.ChatID == card.ChatID && previous.MessageID == card.MessageID &&
		previous.Session == card.Session && card.RenderedFinalAt.Before(previous.RenderedFinalAt) {
		// Page navigation can hide the page containing an already delivered final,
		// but it must not erase the delivery watermark. Otherwise the reconciliation
		// loop mistakes an intentional historical page for a lost final and posts a
		// duplicate carrier back on the latest response.
		card.RenderedFinalAt = previous.RenderedFinalAt
	}
	if exists && previous == card {
		return previous, true, false
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf(
		"%d:%d:%t:%s:%s:%s:%d:%d:%d", card.ChatID, card.MessageID, card.Rich,
		card.RichMediaFileID, card.PaneHash, card.Session.Key(), card.SessionRevision,
		card.SessionEventAt.UnixNano(), card.RenderedFinalAt.UnixNano(),
	)))
	recordCtx := application.WithOperationScope(
		ctx, fmt.Sprintf("telegram-response-card-%d-%d-%x",
			card.ChatID, card.MessageID, fingerprint[:8]),
	)
	if err := h.service.RecordTelegramResponseCard(recordCtx, actor, card); err != nil {
		return previous, exists, false
	}
	return previous, exists, true
}

func screenShowsLatestCardPage(screen telegramui.Screen) bool {
	if len(screen.Grid) == 0 || len(screen.Grid[0]) < 2 ||
		screen.Grid[0][1].Callback.Action != telegramui.ActionPageLatest {
		// Keep-latest mode intentionally hides pagination and always projects the
		// latest page.
		return true
	}
	page, pages, ok := parseCardPageLabel(screen.Grid[0][1].Label)
	return ok && page == pages
}
