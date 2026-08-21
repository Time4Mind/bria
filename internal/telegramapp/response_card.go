package telegramapp

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) editResponseCard(
	ctx context.Context,
	actor application.Principal,
	message telegrambot.Message,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	h.cardMutationMu.Lock()
	defer h.cardMutationMu.Unlock()
	// A background reconciliation may have already promoted this carrier while
	// an older live-card worker was sleeping. Re-read the replicated identity so
	// concurrent recovery paths cannot create two replacement cards.
	current, currentOK, currentErr := h.service.TelegramResponseCard(actor)
	if currentErr == nil && currentOK &&
		current.ChatID == message.ChatID && current.MessageID != 0 &&
		(current.MessageID != message.MessageID || current.Rich != message.Rich ||
			current.RichMediaFileID != message.RichMediaFileID ||
			current.ScreenHash != message.ScreenHash) {
		message = telegramMessage(current)
	}
	active, activeErr := h.service.ActiveSession(actor)
	if activeErr == nil && currentOK && current.Session != active.Ref() {
		return telegramMessage(current), nil
	}
	if activeErr == nil && !h.visibleSessionMatches(actor, active.Ref()) {
		if currentOK {
			return telegramMessage(current), nil
		}
		return message, nil
	}
	if activeErr == nil && !h.screenMatchesRememberedPage(
		actor.UserID, active.Ref(), screen,
	) {
		return message, nil
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
		h.recordResponseCard(ctx, actor, replacement, screen)
		if activeErr == nil {
			h.rememberResolvedCardPage(actor.UserID, active.Ref(), screen)
		}
		return replacement, nil
	}
	edited, err := h.editCardTransportLocked(ctx, message, screen)
	if err == nil {
		h.rememberResponseCardLocked(ctx, actor, edited, screen)
		if activeErr == nil {
			h.rememberResolvedCardPage(actor.UserID, active.Ref(), screen)
		}
	}
	return edited, err
}

func (h *Handler) repostFinalResponseCard(
	ctx context.Context,
	actor application.Principal,
	previous telegrambot.Message,
	ref domain.SessionRef,
	screen telegramui.Screen,
) (telegrambot.Message, error) {
	// Completion can be observed both by the live worker and the heartbeat
	// reconciler. Serialize promotion and compare the replicated carrier so one
	// backend turn creates exactly one new Telegram message.
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	h.cardMutationMu.Lock()
	defer h.cardMutationMu.Unlock()
	current, currentOK, currentErr := h.service.TelegramResponseCard(actor)
	if active, activeErr := h.service.ActiveSession(actor); activeErr != nil ||
		active.Ref() != ref {
		if currentErr == nil && currentOK {
			return telegramMessage(current), nil
		}
		return previous, nil
	}
	if currentErr == nil && currentOK {
		if current.Session != ref {
			return telegramMessage(current), nil
		}
		if checkpoint := screen.Checkpoint; checkpoint != nil &&
			!checkpoint.RenderedFinalAt.IsZero() &&
			responseCardCoversFinal(current, ref, checkpoint.RenderedFinalAt) {
			// Selecting a session can settle a stale running runtime after the
			// callback has already edited the existing carrier with that final.
			// The restored pane worker must not promote the same delivered final
			// into another Telegram message.
			return telegramMessage(current), nil
		}
		if current.ChatID != previous.ChatID || current.MessageID != previous.MessageID ||
			current.Rich != previous.Rich || current.RichMediaFileID != previous.RichMediaFileID {
			return telegramMessage(current), nil
		}
	}
	replacement, err := h.messenger.SendScreen(ctx, previous.ChatID, screen)
	if err != nil {
		return telegrambot.Message{}, err
	}
	if active, activeErr := h.service.ActiveSession(actor); activeErr != nil ||
		active.Ref() != ref {
		_ = h.messenger.DeleteMessage(ctx, replacement)
		if currentErr == nil && currentOK {
			return telegramMessage(current), nil
		}
		return previous, nil
	}
	// The new carrier intentionally opens at the beginning of the completed
	// response. Treat that content position as pinned; only an explicit Latest
	// action may restore follow mode.
	h.rememberResolvedCardPageWithFollow(actor.UserID, ref, screen, false)
	h.recordResponseCard(ctx, actor, replacement, screen)
	current, ok, recordErr := h.service.TelegramResponseCard(actor)
	if recordErr != nil || !ok || current.ChatID != replacement.ChatID ||
		current.MessageID != replacement.MessageID {
		_ = h.messenger.DeleteMessage(ctx, replacement)
		if recordErr != nil {
			return telegrambot.Message{}, recordErr
		}
		return telegrambot.Message{}, errors.New("final response card was not committed")
	}
	// Keep the exact page the user was reading as a frozen history item. Only
	// the newly posted final carrier remains interactive, so stale callbacks
	// cannot compete with it and the previous message is never destroyed.
	h.freezeHistoricalCard(ctx, actor, previous, ref)
	h.rememberResolvedCardPage(actor.UserID, ref, screen)
	return replacement, nil
}
