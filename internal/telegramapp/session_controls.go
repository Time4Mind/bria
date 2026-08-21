package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

type SessionControls interface {
	SendInput(context.Context, application.Principal, string, string) (sessioncontrol.Accepted, error)
	SendExternalInput(context.Context, application.Principal, string, runtimehost.InputPayload) (sessioncontrol.Accepted, error)
	Stop(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error)
	Clear(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error)
	Close(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error)
	Restore(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error)
	OpenTerminal(context.Context, application.Principal, string, domain.SessionRef) (sessioncontrol.Accepted, error)
	CapturePane(context.Context, application.Principal, string, domain.SessionRef) ([]byte, error)
	SendKey(context.Context, application.Principal, string, domain.SessionRef,
		runtimehost.InteractiveKey, string) ([]byte, error)
	Transcript(context.Context, application.Principal, domain.SessionRef) ([]transcript.Event, error)
	OpenSessionFile(context.Context, application.Principal, domain.SessionRef, string) (nodecontrol.SessionFile, error)
}

func isSessionControlAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionStop, telegramui.ActionClose, telegramui.ActionClear,
		telegramui.ActionRestore, telegramui.ActionTerminal, telegramui.ActionConfirmClose,
		telegramui.ActionConfirmClear, telegramui.ActionCancelControl:
		return true
	default:
		return false
	}
}

func (h *Handler) handleSessionControlCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	var restoreTiming *restoreCallbackTiming
	if callback.Action == telegramui.ActionRestore {
		restoreTiming = newRestoreCallbackTiming()
		defer restoreTiming.log()
	}
	if h.controls == nil {
		if restoreTiming != nil {
			restoreTiming.outcome = "controls_unavailable"
		}
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	phaseStartedAt := time.Now()
	ref, err := h.resolveSession(actor, callback.Action, callback.Token)
	if restoreTiming != nil {
		restoreTiming.resolve = time.Since(phaseStartedAt)
		restoreTiming.ref = ref
	}
	if err != nil {
		if restoreTiming != nil {
			restoreTiming.outcome = "resolve_failed"
		}
		return h.controlError(ctx, actor, update.CallbackID, err)
	}
	if restoreTiming != nil {
		if current, sessionErr := h.service.Session(actor, ref); sessionErr == nil {
			restoreTiming.generation = current.RuntimeGeneration + 1
		}
	}
	if callback.Action == telegramui.ActionClose || callback.Action == telegramui.ActionClear {
		screen, renderErr := h.confirmation(actor, ref, callback.Action)
		if renderErr != nil {
			if errors.Is(renderErr, domain.ErrInvalidState) {
				if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
					return err
				}
				return h.editUnavailableSession(ctx, actor, ref, update.CallbackOrigin)
			}
			return h.controlError(ctx, actor, update.CallbackID, renderErr)
		}
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		_, err = h.editExplicitSessionScreen(ctx, actor, update.CallbackOrigin, screen)
		return err
	}
	if callback.Action == telegramui.ActionCancelControl {
		screen, renderErr := h.renderSessionCard(ctx, actor, ref, 0)
		if renderErr != nil {
			return h.controlError(ctx, actor, update.CallbackID, renderErr)
		}
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		var edited telegrambot.Message
		edited, err = h.editExplicitSessionScreen(ctx, actor, update.CallbackOrigin, screen)
		if err == nil {
			h.schedulePaneRefresh(ctx, actor, ref, edited)
		}
		return err
	}

	// Stop the Telegram spinner before network or tmux work. The node executor
	// ACKs after durable enqueue and never waits for the actual terminal action.
	phaseStartedAt = time.Now()
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		if restoreTiming != nil {
			restoreTiming.ack = time.Since(phaseStartedAt)
			restoreTiming.outcome = "callback_ack_failed"
		}
		return err
	}
	if restoreTiming != nil {
		restoreTiming.ack = time.Since(phaseStartedAt)
	}
	operationID := fmt.Sprintf("tg-%d-%s", update.UpdateID, callback.Action)
	switch callback.Action {
	case telegramui.ActionStop:
		_, err = h.controls.Stop(ctx, actor, operationID, ref)
	case telegramui.ActionConfirmClear:
		_, err = h.controls.Clear(ctx, actor, operationID, ref)
	case telegramui.ActionConfirmClose:
		_, err = h.controls.Close(ctx, actor, operationID, ref)
	case telegramui.ActionRestore:
		phaseStartedAt = time.Now()
		_, err = h.controls.Restore(ctx, actor, operationID, ref)
		restoreTiming.control = time.Since(phaseStartedAt)
	case telegramui.ActionTerminal:
		_, err = h.controls.OpenTerminal(ctx, actor, operationID, ref)
	}
	if err != nil {
		if restoreTiming != nil {
			restoreTiming.outcome = "control_failed"
		}
		if (callback.Action == telegramui.ActionConfirmClose ||
			callback.Action == telegramui.ActionConfirmClear) &&
			errors.Is(err, domain.ErrInvalidState) {
			return h.editUnavailableSession(ctx, actor, ref, update.CallbackOrigin)
		}
		if errors.Is(err, sessioncontrol.ErrRuntimeUnavailable) {
			return h.editUnavailableSession(ctx, actor, ref, update.CallbackOrigin)
		}
		return h.controlError(ctx, actor, "", err)
	}
	var screen telegramui.Screen
	cardCtx := ctx
	if callback.Action == telegramui.ActionConfirmClose {
		// CloseSession may select the most recently used live session on the same
		// node. If that node is now empty, preserve the configured session scope:
		// all-hosts returns to the cluster-wide grid, while host-first keeps the
		// closed session's node open.
		if fallback, activeErr := h.service.ActiveSession(actor); activeErr == nil && fallback.NodeID == ref.NodeID {
			screen, err = h.renderSessionCard(ctx, actor, fallback.Ref(), 0)
		} else if activeErr == nil || errors.Is(activeErr, domain.ErrNotFound) {
			var preferences domain.UserPreferences
			preferences, err = h.service.Preferences(actor)
			if err == nil && preferences.SessionView == domain.ViewAllHosts {
				screen, err = h.projector.OpenSessionsWithContext(
					actor, h.cachedContextPercents(),
				)
			} else if err == nil {
				screen, err = h.projector.NodeSessionsWithContext(
					actor, ref.NodeID, h.cachedContextPercents(),
				)
			}
		} else {
			err = activeErr
		}
	} else {
		if restoreTiming != nil {
			phaseStartedAt = time.Now()
			cardCtx = withRestoreTiming(ctx, ref, restoreTiming.generation, "initial")
		}
		screen, err = h.renderSessionCard(cardCtx, actor, ref, 0)
		if restoreTiming != nil {
			restoreTiming.render = time.Since(phaseStartedAt)
		}
	}
	if err != nil {
		if restoreTiming != nil {
			restoreTiming.outcome = "initial_render_failed"
		}
		return err
	}
	var edited telegrambot.Message
	if restoreTiming != nil {
		phaseStartedAt = time.Now()
	}
	if edited, err = h.editExplicitSessionScreen(cardCtx, actor, update.CallbackOrigin, screen); err != nil {
		if restoreTiming != nil {
			restoreTiming.edit = time.Since(phaseStartedAt)
			restoreTiming.outcome = "initial_edit_failed"
		}
		return err
	}
	if restoreTiming != nil {
		restoreTiming.edit = time.Since(phaseStartedAt)
		restoreTiming.outcome = "initial_ready"
	}
	if callback.Action == telegramui.ActionTerminal {
		h.schedulePaneRefresh(ctx, actor, ref, edited)
	}
	if callback.Action == telegramui.ActionStop || callback.Action == telegramui.ActionConfirmClear {
		go h.refreshSettledCard(ctx, actor, ref, operationID, update.CallbackOrigin)
	}
	if callback.Action == telegramui.ActionRestore {
		go h.refreshRestoredCard(
			ctx, actor, ref, update.CallbackOrigin,
			restoreTiming.startedAt, time.Now(), restoreTiming.generation,
		)
	}
	return nil
}

func (h *Handler) confirmation(
	actor application.Principal,
	ref domain.SessionRef,
	action telegramui.Action,
) (telegramui.Screen, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil {
		return telegramui.Screen{}, err
	}
	requiredAction := domain.ActionClose
	if action == telegramui.ActionClear {
		requiredAction = domain.ActionClear
	}
	if err := h.service.RequireSessionAction(actor, ref, requiredAction); err != nil {
		return telegramui.Screen{}, err
	}
	confirmAction := telegramui.ActionConfirmClose
	labelKey, textKey := i18n.ButtonConfirmClose, i18n.ConfirmClose
	if action == telegramui.ActionClear {
		confirmAction, labelKey, textKey = telegramui.ActionConfirmClear,
			i18n.ButtonConfirmClear, i18n.ConfirmClear
	}
	confirmToken, err := h.tokens.Session(actor.UserID, confirmAction, ref)
	if err != nil {
		return telegramui.Screen{}, err
	}
	cancelToken, err := h.tokens.Session(actor.UserID, telegramui.ActionCancelControl, ref)
	if err != nil {
		return telegramui.Screen{}, err
	}
	copy := h.copy(actor)
	return telegramui.RenderConfirmation(telegramui.ConfirmationInput{
		Copy: copy, Text: copy.Format(textKey, session.Name),
		ConfirmLabel: copy.Text(labelKey), ConfirmAction: confirmAction,
		ConfirmToken: confirmToken, CancelToken: cancelToken,
	}), nil
}

func (h *Handler) controlError(
	ctx context.Context,
	actor application.Principal,
	callbackID string,
	err error,
) error {
	if safeDrop(err) || errors.Is(err, domain.ErrInvalidState) ||
		errors.Is(err, domain.ErrStaleOperation) ||
		errors.Is(err, sessioncontrol.ErrRuntimeUnavailable) {
		return h.answerAndDrop(ctx, callbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	return err
}
