package telegramapp

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) handleCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
) error {
	callback, err := telegramui.DecodeCallback(update.CallbackData)
	if err != nil {
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	if callback.Action == telegramui.ActionNoop {
		return h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, "")
	}
	if !isClusterUpdateAction(callback.Action) {
		h.cancelClusterUpdateRefresh(actor.UserID)
	}
	// Telegram callback payloads do not consistently echo rich_message. Recover
	// the durable carrier metadata when the callback belongs to the current
	// response card, otherwise a rich card is mistaken for a legacy carrier and
	// every rich session selection becomes send+delete instead of an edit.
	update.CallbackOrigin = h.resolveCallbackCarrier(actor, update.CallbackOrigin)
	if leavesSessionCard(callback.Action) {
		h.cancelPaneRefresh(actor.UserID)
	}
	if callback.Action == telegramui.ActionMenu || callback.Action == telegramui.ActionSettings ||
		callback.Action == telegramui.ActionSettingsCategory || callback.Action == telegramui.ActionStatusMode {
		if callback.Action != telegramui.ActionStatusMode || callback.Token != "settings" {
			h.cancelProviderAuthFlow(ctx, actor)
		}
		h.clearMembershipFlows(actor)
	}
	if isCreateAction(callback.Action) {
		// Session creation may browse another node. Clear Telegram's spinner
		// before any language write or node-control round trip.
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		if err := h.ensureLanguage(ctx, actor, update.LanguageCode); err != nil {
			if safeDrop(err) {
				return nil
			}
			return err
		}
		return h.handleCreateCallback(ctx, actor, update, callback)
	}
	if !settingMutation(callback.Action) {
		if err := h.ensureLanguage(ctx, actor, update.LanguageCode); err != nil {
			if safeDrop(err) {
				return h.answerAndDrop(ctx, update.CallbackID,
					h.copy(actor).Text(i18n.ToastUnavailable))
			}
			return err
		}
	}
	if isSessionControlAction(callback.Action) {
		return h.handleSessionControlCallback(ctx, actor, update, callback)
	}
	if isInteractiveAction(callback.Action) {
		return h.handleInteractiveCallback(ctx, actor, update, callback)
	}
	if isMembershipLifecycleAction(callback.Action) {
		return h.handleMembershipLifecycle(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionProviderAuth ||
		callback.Action == telegramui.ActionProviderAuthCancel {
		return h.handleProviderAuthCallback(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionProviderAlias {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		screen, err := h.beginProviderAlias(actor, callback.Token)
		if safeDrop(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
		return err
	}
	if callback.Action == telegramui.ActionBackendConnect ||
		callback.Action == telegramui.ActionBackendDisconnect {
		return h.handleNodeBackendCallback(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionBackendInstall {
		return h.handleNodeBackendInstallCallback(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionSelectSession {
		// Validate the target before the early acknowledgement below. A session
		// can be archived between rendering its button and receiving the click;
		// that callback is terminal, not a reason to retry the Telegram update.
		ref, resolveErr := h.resolveSession(actor, callback.Action, callback.Token)
		if resolveErr == nil {
			var session domain.Session
			session, resolveErr = h.service.Session(actor, ref)
			if resolveErr == nil && !session.IsLive() {
				resolveErr = domain.ErrInvalidState
			}
		}
		if safeDrop(resolveErr) || errors.Is(resolveErr, domain.ErrInvalidState) {
			return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
		}
		if resolveErr != nil {
			return resolveErr
		}
	}
	// Telegram keeps a callback spinner visible until AnswerCallbackQuery returns.
	// Preference writes may wait for a Raft quorum, so acknowledge those clicks
	// before entering the replicated mutation path. The card itself is still
	// edited only from committed state below.
	answeredEarly := settingMutation(callback.Action) ||
		callback.Action == telegramui.ActionSelectSession ||
		callback.Action == telegramui.ActionSelectNode ||
		isStatusAction(callback.Action) ||
		isArchiveContentAction(callback.Action) ||
		isEnrollmentAction(callback.Action) || isClusterUpdateAction(callback.Action) ||
		isClusterHealthAction(callback.Action) ||
		callback.Action == telegramui.ActionPagePrevious ||
		callback.Action == telegramui.ActionPageLatest ||
		callback.Action == telegramui.ActionPageNext || isListPageAction(callback.Action)
	if answeredEarly {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
	}
	return h.handleNavigationCallback(ctx, actor, update, callback, answeredEarly)
}

func (h *Handler) selectArchiveNode(
	ctx context.Context,
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	candidates, err := h.service.CallbackNodeCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeID, err := h.tokens.ResolveNode(
		actor.UserID, telegramui.ActionSelectArchiveNode, token, candidates,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.SelectNode(ctx, actor, nodeID); err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.NodeArchives(actor, nodeID)
}
