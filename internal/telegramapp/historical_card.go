package telegramapp

import (
	"context"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// freezeHistoricalCard removes stale controls without making an old session
// card look broken. Its single safe callback reopens the session through the
// normal authorization and current-state projection path.
func (h *Handler) freezeHistoricalCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	ref domain.SessionRef,
) {
	current, ok, err := h.service.TelegramResponseCard(actor)
	if err != nil || !ok || current.ChatID != message.ChatID {
		return
	}
	if current.MessageID == message.MessageID {
		processlog.Outcomef(processlog.Detail, "skipped_current",
			"bria telegram: keyboard_freeze message_id=%d outcome=skipped_current",
			message.MessageID,
		)
		return
	}
	grid := telegramui.Grid(nil)
	if ref.Validate() == nil {
		if _, sessionErr := h.service.Session(actor, ref); sessionErr == nil {
			token, tokenErr := h.tokens.Session(
				actor.UserID, telegramui.ActionSelectSession, ref,
			)
			if tokenErr == nil {
				grid = telegramui.Grid{telegramui.Row{{
					Label: h.copy(actor).Text(i18n.ButtonOpenCurrent),
					Callback: telegramui.Callback{
						Action: telegramui.ActionSelectSession, Token: token,
					},
				}}}
			}
		}
	}
	if replacer, replaceOK := h.messenger.(interface {
		ReplaceKeyboard(context.Context, telegrambot.Message, telegramui.Grid) error
	}); replaceOK && len(grid) > 0 {
		err = replacer.ReplaceKeyboard(ctx, message, grid)
	} else {
		err = h.messenger.ClearKeyboard(ctx, message)
	}
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	processlog.Failuref(
		processlog.Detail, outboundFailureClass(err),
		"bria telegram: keyboard_freeze message_id=%d current_id=%d buttons=%d outcome=%s",
		message.MessageID, current.MessageID, len(grid), outcome,
	)
}
