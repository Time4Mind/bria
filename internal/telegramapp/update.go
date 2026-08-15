package telegramapp

import (
	"context"
	"fmt"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
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
	policy, err := h.service.LeaderPolicy(actor)
	if err != nil {
		return err
	}
	if policy.EffectiveMode() == domain.LeaderSelectionManual && policy.NodeID == "" {
		if !leaderSetupAction(update) {
			return h.requireLeaderSetup(ctx, actor, update)
		}
	}
	switch update.Kind {
	case telegrambot.IncomingMessage:
		return h.handleMessage(ctx, actor, update)
	case telegrambot.IncomingCallback:
		return h.handleCallback(ctx, actor, update)
	default:
		return nil
	}
}

func leaderSetupAction(update telegrambot.IncomingUpdate) bool {
	if update.Kind != telegrambot.IncomingCallback {
		return false
	}
	callback, err := telegramui.DecodeCallback(update.CallbackData)
	if err != nil {
		return false
	}
	switch callback.Action {
	case telegramui.ActionSetLeaderMode, telegramui.ActionSetLeaderNode,
		telegramui.ActionConfirmLeader:
		return true
	case telegramui.ActionOpenSetting:
		setting := telegramui.SettingID(callback.Token)
		return setting == telegramui.SettingLeaderMode || setting == telegramui.SettingLeaderNode
	default:
		return false
	}
}

func (h *Handler) requireLeaderSetup(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
) error {
	if err := h.ensureLanguage(ctx, actor, update.LanguageCode); err != nil && !safeDrop(err) {
		return err
	}
	screen, err := h.projector.Setting(actor, telegramui.SettingLeaderMode)
	if err != nil {
		return err
	}
	if update.Kind != telegrambot.IncomingCallback {
		return h.sendProjected(ctx, update.ChatID, screen, nil)
	}
	if update.CallbackID != "" {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
	}
	_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
	return err
}
