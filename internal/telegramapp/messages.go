package telegramapp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
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
	if h.controls == nil || (update.Content.Kind == telegrambot.IncomingText &&
		(text == "" || text == "/start" || text == "/menu")) {
		screen, err := h.projector.MainMenu(actor)
		return h.sendProjected(ctx, update.ChatID, screen, err)
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
	// The prompt is already durably queued. Render replicated state immediately;
	// transcript and terminal refresh stay off the Telegram request path.
	screen, err := h.projector.SessionCard(actor, accepted.Session)
	if err == nil && update.Content.Kind == telegrambot.IncomingVoice && !accepted.Deferred {
		screen.Text += "\n\n" + h.copy(actor).Text(i18n.VoiceQueued)
	}
	message, err := h.sendProjectedMessage(ctx, update.ChatID, screen, err)
	if err == nil {
		h.schedulePaneRefresh(ctx, actor, accepted.Session, message)
		if accepted.NamingQueued {
			h.scheduleNameRefresh(ctx, actor, accepted.Session, message)
		}
		h.rememberResponseCard(ctx, actor, message)
	}
	return err
}

func (h *Handler) sendIncomingInput(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
) (sessioncontrol.Accepted, error) {
	operationID := fmt.Sprintf("tg-%d-input", update.UpdateID)
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

func (h *Handler) rememberResponseCard(
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
	if exists && previous == card {
		return previous, true, false
	}
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf(
		"%d:%d:%t:%s:%s", card.ChatID, card.MessageID, card.Rich,
		card.RichMediaFileID, card.PaneHash,
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

func (h *Handler) editResponseCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	// A background reconciliation may have already promoted this carrier while
	// an older live-card worker was sleeping. Re-read the replicated identity so
	// concurrent recovery paths cannot create two replacement cards.
	if current, ok, err := h.service.TelegramResponseCard(actor); err == nil && ok &&
		current.ChatID == message.ChatID && current.MessageID != 0 &&
		(current.MessageID != message.MessageID || current.Rich != message.Rich ||
			current.RichMediaFileID != message.RichMediaFileID) {
		message = telegramMessage(current)
	}
	if !message.Rich && (screen.RichMarkdown || screen.Pane != nil) {
		// Rich Markdown is a distinct Telegram carrier. CCBot promotes a legacy
		// live card instead of permanently downgrading every later tool spoiler;
		// mirror that behavior by replacing the carrier once and retaining the
		// Rich message for subsequent edits.
		replacement, err := h.messenger.SendScreen(ctx, message.ChatID, screen)
		if err != nil {
			return telegrambot.Message{}, err
		}
		_ = h.messenger.DeleteMessage(ctx, message)
		h.recordResponseCard(ctx, actor, replacement)
		return replacement, nil
	}
	edited, err := h.messenger.EditScreen(ctx, message, screen)
	if err == nil {
		h.rememberResponseCard(ctx, actor, edited)
	}
	return edited, err
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
	return h.messenger.SendScreen(ctx, chatID, screen)
}
