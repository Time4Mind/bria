package telegramcontroller

import (
	"context"
	"strconv"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramturnhelpers"
)

func (c *Controller) queueRecoveryInput(ctx context.Context, update coordinator.Update, id domain.SessionID) (coordinator.Decision, error) {
	// The accepted predecessor still owns the visible prompt/history. Keep the
	// successor in journal custody without inserting a new prompt or submitting.
	input, rejection := c.preparation.Prepare(ctx, update)
	if rejection != "" {
		return c.cardDecision(ctx, id, rejection)
	}
	messageID := "telegram-update:" + strconv.FormatInt(update.ID, 10)
	if err := telegramturnhelpers.AcceptInput(ctx, c.durableInput, id, messageID, input, c.preparation.Payload(ctx, input.Text)); err != nil {
		return c.cardDecision(ctx, id, "Не удалось сохранить запрос. Он не отправлен CLI.")
	}
	return c.cardDecision(ctx, id, "")
}
