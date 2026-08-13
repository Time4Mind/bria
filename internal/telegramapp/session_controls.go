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
	if h.controls == nil {
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	ref, err := h.resolveSession(actor, callback.Action, callback.Token)
	if err != nil {
		return h.controlError(ctx, actor, update.CallbackID, err)
	}
	if callback.Action == telegramui.ActionClose || callback.Action == telegramui.ActionClear {
		screen, renderErr := h.confirmation(actor, ref, callback.Action)
		if renderErr != nil {
			return h.controlError(ctx, actor, update.CallbackID, renderErr)
		}
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
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
		edited, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
		if err == nil {
			h.schedulePaneRefresh(ctx, actor, ref, edited)
		}
		return err
	}

	// Stop the Telegram spinner before network or tmux work. The node executor
	// ACKs after durable enqueue and never waits for the actual terminal action.
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		return err
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
		_, err = h.controls.Restore(ctx, actor, operationID, ref)
	case telegramui.ActionTerminal:
		_, err = h.controls.OpenTerminal(ctx, actor, operationID, ref)
	}
	if err != nil {
		if errors.Is(err, sessioncontrol.ErrRuntimeUnavailable) {
			return h.editUnavailableSession(ctx, actor, ref, update.CallbackOrigin)
		}
		return h.controlError(ctx, actor, "", err)
	}
	var screen telegramui.Screen
	if callback.Action == telegramui.ActionConfirmClose {
		// CloseSession may select the most recently used live session on the same
		// node. If that node is now empty, keep its Sessions screen open rather
		// than switching hosts or leaving the archived card on screen.
		if fallback, activeErr := h.service.ActiveSession(actor); activeErr == nil && fallback.NodeID == ref.NodeID {
			screen, err = h.renderSessionCard(ctx, actor, fallback.Ref(), 0)
		} else if activeErr == nil || errors.Is(activeErr, domain.ErrNotFound) {
			screen, err = h.projector.NodeSessions(actor, ref.NodeID)
		} else {
			err = activeErr
		}
	} else {
		screen, err = h.renderSessionCard(ctx, actor, ref, 0)
	}
	if err != nil {
		return err
	}
	if _, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen); err != nil {
		return err
	}
	if callback.Action == telegramui.ActionStop || callback.Action == telegramui.ActionConfirmClear {
		go h.refreshSettledCard(ctx, actor, ref, operationID, update.CallbackOrigin)
	}
	if callback.Action == telegramui.ActionRestore {
		go h.refreshRestoredCard(ctx, actor, ref, update.CallbackOrigin)
	}
	return nil
}

func (h *Handler) refreshRestoredCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	origin telegrambot.Message,
) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(35 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if !h.canRefresh() {
				return
			}
			session, err := h.service.Session(actor, ref)
			if err != nil || session.ResumePending {
				continue
			}
			screen, err := h.renderSessionCard(ctx, actor, ref, 0)
			if err == nil {
				_, _ = h.messenger.EditScreen(ctx, origin, screen)
			}
			return
		}
	}
}

func (h *Handler) editUnavailableSession(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	origin telegrambot.Message,
) error {
	screen, err := h.renderSessionCard(ctx, actor, ref, 0)
	if err != nil {
		return nil
	}
	screen.Text += "\n\n" + h.copy(actor).Text(i18n.ToastUnavailable)
	_, err = h.messenger.EditScreen(ctx, origin, screen)
	return err
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

func (h *Handler) refreshSettledCard(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
	operationID string,
	origin telegrambot.Message,
) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if !h.canRefresh() {
				return
			}
			session, err := h.service.Session(actor, ref)
			if err != nil || session.LastOperation == nil ||
				session.LastOperation.OperationID != operationID ||
				session.LastOperation.Status == domain.OperationQueued {
				continue
			}
			screen, err := h.renderSessionCard(ctx, actor, ref, 0)
			if err == nil {
				_, _ = h.messenger.EditScreen(ctx, origin, screen)
			}
			return
		}
	}
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
