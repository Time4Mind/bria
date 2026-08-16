package telegramapp

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
)

func (h *Handler) rememberResponseCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
) {
	h.cardMutationMu.Lock()
	defer h.cardMutationMu.Unlock()
	h.rememberResponseCardLocked(ctx, actor, message)
}

func (h *Handler) rememberResponseCardLocked(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
) {
	previous, exists, changed := h.recordResponseCard(ctx, actor, message)
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
) (domain.TelegramResponseCard, bool, bool) {
	previous, exists, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return domain.TelegramResponseCard{}, false, false
	}
	card := domain.TelegramResponseCard{
		ChatID: message.ChatID, MessageID: message.MessageID, Rich: message.Rich,
		RichMediaFileID: message.RichMediaFileID, PaneHash: message.PaneHash,
	}
	if session, sessionErr := h.service.ActiveSession(actor); sessionErr == nil {
		card.Session = session.Ref()
		card.SessionRevision = session.Revision
	}
	if exists && previous == card {
		return previous, true, false
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf(
		"%d:%d:%t:%s:%s:%s:%d", card.ChatID, card.MessageID, card.Rich,
		card.RichMediaFileID, card.PaneHash, card.Session.Key(), card.SessionRevision,
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
