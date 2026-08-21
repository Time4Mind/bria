package telegramapp

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func sameProviderRuntime(current, expected domain.Session) bool {
	return current.Ref() == expected.Ref() &&
		current.RuntimeGeneration == expected.RuntimeGeneration &&
		current.ProviderSessionID == expected.ProviderSessionID
}

func runtimeCanDeliverFinal(session domain.Session) bool {
	return session.RuntimePhase == domain.RuntimeIdle ||
		session.RuntimePhase == domain.RuntimeDegraded
}

// deliverActiveFinal publishes a completion only when its session is the
// actor's current visible session. Background sessions remain transcript-only
// until an explicit selection renders their canonical transcript.
func (h *Handler) deliverActiveFinal(
	ctx context.Context,
	actor application.Principal,
	expected domain.Session,
	finalAt time.Time,
) (bool, string) {
	latest, err := h.service.Session(actor, expected.Ref())
	if err != nil || !sameProviderRuntime(latest, expected) {
		return true, "ignored"
	}
	if !runtimeCanDeliverFinal(latest) {
		return false, "runtime_pending"
	}
	active, err := h.service.ActiveSession(actor)
	if err != nil || active.Ref() != latest.Ref() {
		return true, "background_settled"
	}
	card, present, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return false, "card_unavailable"
	}
	view := h.visibleCardSnapshot(actor.UserID)
	if !finalMaySurfaceOverView(view, card, present, latest.Ref()) {
		return true, "not_visible"
	}
	if present && responseCardCoversFinal(card, latest.Ref(), finalAt) {
		return true, "already_delivered"
	}
	snapshot, err := h.renderSessionCardSnapshot(
		ctx, actor, latest.Ref(), application.CardPageLatestResponseStart,
	)
	if err != nil {
		return false, "card_unavailable"
	}
	snapshotFinalAt, final := finalTranscriptAt(snapshot.events)
	if !final || snapshotFinalAt.Before(finalAt) {
		return false, "final_pending"
	}
	return h.deliverActiveFinalScreen(
		ctx, actor, latest, snapshotFinalAt, snapshot.screen,
	)
}

func (h *Handler) deliverActiveFinalScreen(
	ctx context.Context,
	actor application.Principal,
	expected domain.Session,
	finalAt time.Time,
	screen telegramui.Screen,
) (bool, string) {
	latest, err := h.service.Session(actor, expected.Ref())
	if err != nil || !sameProviderRuntime(latest, expected) {
		return true, "ignored"
	}
	if !runtimeCanDeliverFinal(latest) {
		return false, "runtime_pending"
	}
	active, err := h.service.ActiveSession(actor)
	if err != nil || active.Ref() != latest.Ref() {
		return true, "background_settled"
	}
	card, present, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return false, "card_unavailable"
	}
	view := h.visibleCardSnapshot(actor.UserID)
	if !finalMaySurfaceOverView(view, card, present, latest.Ref()) {
		return true, "not_visible"
	}
	if present && responseCardCoversFinal(card, latest.Ref(), finalAt) {
		return true, "already_delivered"
	}
	h.cancelPaneRefresh(actor.UserID)
	if present && card.Session == latest.Ref() {
		_, err = h.repostFinalResponseCard(
			ctx, actor, telegramMessage(card), latest.Ref(), screen,
		)
	} else {
		err = h.publishRecoveredFinalCard(
			ctx, actor, latest, finalAt, screen,
		)
	}
	if err != nil {
		return false, "delivery_pending"
	}
	card, present, err = h.service.TelegramResponseCard(actor)
	if err == nil && present && responseCardCoversFinal(card, latest.Ref(), finalAt) {
		return true, "delivered"
	}
	return false, "delivery_pending"
}

func (h *Handler) publishRecoveredFinalCard(
	ctx context.Context,
	actor application.Principal,
	expected domain.Session,
	finalAt time.Time,
	screen telegramui.Screen,
) error {
	h.cardEditMu.Lock()
	defer h.cardEditMu.Unlock()
	h.cardMutationMu.Lock()
	defer h.cardMutationMu.Unlock()

	latest, err := h.service.Session(actor, expected.Ref())
	if err != nil || !sameProviderRuntime(latest, expected) ||
		!runtimeCanDeliverFinal(latest) {
		return domain.ErrInvalidState
	}
	active, err := h.service.ActiveSession(actor)
	if err != nil || active.Ref() != expected.Ref() {
		return domain.ErrInvalidState
	}
	current, exists, err := h.service.TelegramResponseCard(actor)
	if err != nil {
		return err
	}
	view := h.visibleCardSnapshot(actor.UserID)
	if !finalMaySurfaceOverView(view, current, exists, expected.Ref()) {
		return domain.ErrInvalidState
	}
	if exists && responseCardCoversFinal(current, expected.Ref(), finalAt) {
		return nil
	}
	replacement, err := h.messenger.SendScreen(ctx, int64(actor.UserID), screen)
	if err != nil {
		return err
	}
	deleteReplacement := func(result error) error {
		_ = h.messenger.DeleteMessage(ctx, replacement)
		return result
	}
	latest, err = h.service.Session(actor, expected.Ref())
	if err != nil || !sameProviderRuntime(latest, expected) ||
		!runtimeCanDeliverFinal(latest) {
		return deleteReplacement(domain.ErrInvalidState)
	}
	active, err = h.service.ActiveSession(actor)
	if err != nil || active.Ref() != expected.Ref() ||
		h.visibleCardSnapshot(actor.UserID) != view {
		return deleteReplacement(domain.ErrInvalidState)
	}
	h.rememberResolvedCardPageWithFollow(actor.UserID, expected.Ref(), screen, false)
	h.rememberResponseCardLocked(ctx, actor, replacement, screen)
	committed, ok, commitErr := h.service.TelegramResponseCard(actor)
	if commitErr != nil || !ok || committed.ChatID != replacement.ChatID ||
		committed.MessageID != replacement.MessageID ||
		!responseCardCoversFinal(committed, expected.Ref(), finalAt) {
		if commitErr != nil {
			return deleteReplacement(commitErr)
		}
		return deleteReplacement(errors.New("recovered final response card was not committed"))
	}
	return nil
}
