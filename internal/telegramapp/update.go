package telegramapp

import (
	"context"
	"fmt"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
)

func (h *Handler) HandleTelegramUpdate(
	ctx context.Context,
	update telegrambot.IncomingUpdate,
) error {
	if update.Kind == telegrambot.IncomingMessage && h.activity != nil {
		h.activity.observeIncoming(update.ChatID, update.MessageID)
	}
	actor := application.Principal{UserID: domain.UserID(update.UserID)}
	if !h.service.IsOwner(actor) {
		if update.Kind == telegrambot.IncomingCallback && update.CallbackID != "" {
			return h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, "")
		}
		return nil
	}
	ctx = application.WithOperationScope(ctx, fmt.Sprintf("telegram-update-%d", update.UpdateID))
	switch update.Kind {
	case telegrambot.IncomingMessage:
		return h.handleMessage(ctx, actor, update)
	case telegrambot.IncomingCallback:
		return h.handleCallback(ctx, actor, update)
	default:
		return nil
	}
}
