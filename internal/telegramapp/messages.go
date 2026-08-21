package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) handleMessage(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
) error {
	if err := h.ensureLanguage(ctx, actor, update.LanguageCode); err != nil {
		if safeDrop(err) {
			return nil
		}
		return err
	}
	text := strings.TrimSpace(update.Text)
	if h.awaitingProviderAuthCode(actor) {
		return h.acceptProviderAuthCode(ctx, actor, update, text)
	}
	if h.awaitingProviderAlias(actor) {
		return h.acceptProviderAlias(ctx, actor, update.ChatID, text)
	}
	if h.awaitingNodeRename(actor) {
		return h.acceptNodeRename(ctx, actor, update.ChatID, text)
	}
	if h.awaitingNodeContract(actor) {
		return h.acceptNodeContract(ctx, actor, update.ChatID, text)
	}
	textInput := update.Content.Kind == "" || update.Content.Kind == telegrambot.IncomingText
	if h.controls == nil || (textInput &&
		(text == "" || text == "/start" || text == "/menu")) {
		if text == "/start" || text == "/menu" {
			h.clearCreateFlow(actor.UserID)
		}
		screen, err := h.projector.MainMenu(actor)
		return h.sendProjected(ctx, update.ChatID, screen, err)
	}
	createActive, createReady, err := h.prepareCreateFlowInput(ctx, actor)
	if err != nil {
		return err
	}
	if createActive && !createReady {
		processlog.Detailf(
			"bria telegram: input_blocked update_id=%d content_kind=%q reason=create_flow_incomplete",
			update.UpdateID, update.Content.Kind,
		)
		return h.sendProjected(
			ctx, update.ChatID, telegramui.RenderCreateInputBlocked(h.copy(actor)), nil,
		)
	}
	voiceBaseline := voicePendingBaseline{}
	inputBaseline := inputPendingBaseline{}
	if update.Content.Kind == telegrambot.IncomingVoice {
		preferences, preferencesErr := h.service.Preferences(actor)
		if preferencesErr != nil {
			return preferencesErr
		}
		if preferences.EffectiveVoiceBackend() == domain.VoiceOff {
			plans, planErr := h.voiceEnablePlans(actor)
			if planErr != nil {
				return planErr
			}
			return h.sendProjected(ctx, update.ChatID,
				telegramui.RenderVoiceInputEnableConfirmation(h.copy(actor), plans), nil)
		}
		voiceBaseline = h.captureVoiceBaseline(ctx, actor)
	} else if pendingInputText(update) != "" {
		inputBaseline = h.captureInputBaseline(ctx, actor)
	}
	accepted, err := h.sendIncomingInput(ctx, actor, update)
	if errors.Is(err, domain.ErrQueueFull) {
		screen, projectErr := h.projector.SessionCard(actor, accepted.Session)
		if projectErr != nil {
			return nil
		}
		preferences, _ := h.service.Preferences(actor)
		limit := preferences.EffectiveOfflineInputQueueLimit()
		screen.Text += "\n\n" + h.copy(actor).Format(i18n.CardOfflineQueueFull, limit, limit)
		return h.sendProjected(ctx, update.ChatID, screen, nil)
	}
	if errors.Is(err, sessioncontrol.ErrRuntimeUnavailable) {
		screen, projectErr := h.renderSessionCard(ctx, actor, accepted.Session, 0)
		if projectErr != nil {
			return nil
		}
		screen.Text += "\n\n" + h.copy(actor).Text(i18n.ToastUnavailable)
		return h.sendProjected(ctx, update.ChatID, screen, nil)
	}
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidState) {
		screen, projectErr := h.projector.OpenSessions(actor)
		return h.sendProjected(ctx, update.ChatID, screen, projectErr)
	}
	if safeDrop(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if inputBaseline.ref == accepted.Session || voiceBaseline.ref == accepted.Session {
		h.restoreFollowForInput(actor.UserID, accepted.Session)
	}
	// The prompt is already durably queued. Reuse the exact transcript snapshot
	// that backed the previous card, but do not invent a chronological user row:
	// only the provider transcript knows where the CLI processed it relative to
	// tools and responses. Voice has its own pending transcription status row.
	screen, err := h.projector.SessionCard(actor, accepted.Session)
	if err == nil && update.Content.Kind == telegrambot.IncomingVoice && !accepted.Deferred {
		h.markVoicePending(actor, accepted.Session, operationIDForInput(update.UpdateID), voiceBaseline)
		screen, err = h.pendingVoiceCard(actor, accepted.Session, voiceBaseline)
	} else if err == nil && pendingInputText(update) != "" {
		screen, err = h.pendingInputCard(actor, accepted.Session, inputBaseline)
	}
	message, err := h.sendProjectedMessage(ctx, update.ChatID, screen, err)
	if err == nil {
		h.schedulePaneRefresh(ctx, actor, accepted.Session, message)
		if accepted.NamingQueued {
			h.scheduleNameRefresh(ctx, actor, accepted.Session, message)
		}
	}
	return err
}

func (h *Handler) sendIncomingInput(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
) (sessioncontrol.Accepted, error) {
	operationID := operationIDForInput(update.UpdateID)
	if update.Content.Kind == "" || update.Content.Kind == telegrambot.IncomingText {
		return h.controls.SendInput(ctx, actor, operationID, update.Text)
	}
	kind, ok := runtimeInputKind(update.Content.Kind)
	if !ok {
		return sessioncontrol.Accepted{}, domain.ErrInvalidState
	}
	payload := runtimehost.InputPayload{
		Kind: kind, Caption: update.Text,
		File: runtimehost.InputFile{
			Provider: "telegram", ID: update.Content.FileID,
			UniqueID: update.Content.FileUniqueID, Name: update.Content.FileName,
			MIMEType: update.Content.MIMEType, Size: update.Content.FileSize,
		},
	}
	if kind == runtimehost.InputVoice {
		preferences, preferencesErr := h.service.Preferences(actor)
		if preferencesErr != nil {
			return sessioncontrol.Accepted{}, preferencesErr
		}
		if preferences.EffectiveVoiceBackend() == domain.VoiceOff {
			return sessioncontrol.Accepted{}, domain.ErrInvalidState
		}
		payload.VoiceBackend = string(preferences.EffectiveVoiceBackend())
		language := preferences.EffectiveLanguage()
		if language == domain.LanguageAuto {
			language = domain.LanguageFromTelegram(update.LanguageCode)
		}
		payload.VoiceLanguage = string(language)
	}
	return h.controls.SendExternalInput(ctx, actor, operationID, payload)
}

func operationIDForInput(updateID int64) string {
	return fmt.Sprintf("tg-%d-input", updateID)
}

func runtimeInputKind(kind telegrambot.IncomingContentKind) (runtimehost.InputKind, bool) {
	switch kind {
	case telegrambot.IncomingPhoto:
		return runtimehost.InputPhoto, true
	case telegrambot.IncomingDocument:
		return runtimehost.InputDocument, true
	case telegrambot.IncomingVoice:
		return runtimehost.InputVoice, true
	default:
		return "", false
	}
}

func (h *Handler) sendProjected(
	ctx context.Context,
	chatID int64,
	screen telegramui.Screen,
	err error,
) error {
	_, err = h.sendProjectedMessage(ctx, chatID, screen, err)
	return err
}

func (h *Handler) sendProjectedMessage(
	ctx context.Context,
	chatID int64,
	screen telegramui.Screen,
	err error,
) (telegrambot.Message, error) {
	if safeDrop(err) {
		return telegrambot.Message{}, nil
	}
	if err != nil {
		return telegrambot.Message{}, err
	}
	userID := domain.UserID(chatID)
	viewChange := h.beginVisibleScreen(userID, screen)
	message, err := h.messenger.SendScreen(ctx, chatID, screen)
	if err != nil {
		h.rollbackVisibleScreen(viewChange)
	} else {
		// Every projected screen is the current interactive carrier, including
		// menus and create/setup flows. Persisting non-session carriers makes the
		// visibility gate survive a process or leader restart, so a delayed final
		// cannot surface over a menu that is still visible in Telegram.
		h.rememberResponseCard(ctx, application.Principal{UserID: userID}, message, screen)
	}
	return message, err
}
