package telegramapp

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/domain"
)

func (h *Handler) answerAndDrop(ctx context.Context, callbackID, text string) error {
	if callbackID == "" {
		return nil
	}
	return h.messenger.AnswerCallbackQuery(ctx, callbackID, text)
}

func safeDrop(err error) bool {
	return errors.Is(err, domain.ErrAccessDenied) || errors.Is(err, domain.ErrNotFound)
}
