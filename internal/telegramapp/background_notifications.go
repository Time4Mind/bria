package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const backgroundSettleWorkers = 8

func (h *Handler) RunBackgroundNotifications(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 1200 * time.Millisecond
	}
	panelFingerprints := make(map[domain.UserID]string)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if h.canRefresh() {
			// Keep runtime settlement and card delivery in one ordered pass. Two
			// independent loops can observe the same revision in opposite order,
			// allowing a routine panel edit to consume a just-finished turn before
			// the completion carrier is reposted.
			h.settleRunningSessions(ctx)
			h.reconcileActiveFinalCards(ctx)
			h.restoreActivePaneRefreshes(ctx)
			h.scanBackgroundNotifications(ctx, panelFingerprints)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reconcileActiveFinalCards closes the race between node heartbeat settlement
// and Telegram settlement. A heartbeat can observe the provider final first and
// move the runtime to idle; that removes it from RunningSessions before the live
// card worker has published the final answer. The replicated card revision makes
// the repair durable across worker, leader, and process restarts.
func (h *Handler) reconcileActiveFinalCards(ctx context.Context) {
	if h.controls == nil {
		return
	}
	for _, userID := range h.service.BackgroundPanelUsers() {
		actor := application.Principal{UserID: userID}
		card, ok, err := h.service.TelegramResponseCard(actor)
		if err != nil || !ok {
			continue
		}
		session, err := h.service.ActiveSession(actor)
		if err != nil || session.RuntimePhase != domain.RuntimeIdle {
			continue
		}
		if card.Session != session.Ref() {
			continue
		}
		if card.Session == session.Ref() && card.SessionRevision >= session.Revision &&
			!card.RenderedFinalAt.IsZero() {
			continue
		}
		snapshot, err := h.renderSessionCardSnapshot(
			ctx, actor, session.Ref(), application.CardPageLatestResponseStart,
		)
		if err != nil {
			continue
		}
		finalAt, final := finalTranscriptAt(snapshot.events)
		if !final || !transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
			continue
		}
		if responseCardCoversFinal(card, session.Ref(), finalAt) {
			continue
		}
		_, _ = h.repostFinalResponseCard(
			ctx, actor, telegramMessage(card), session.Ref(), snapshot.screen,
		)
	}
}

func (h *Handler) restoreActivePaneRefreshes(ctx context.Context) {
	for _, candidate := range h.service.RunningSessions() {
		active, err := h.service.ActiveSession(candidate.Actor)
		if err != nil || active.Ref() != candidate.Session.Ref() {
			continue
		}
		card, ok, err := h.service.TelegramResponseCard(candidate.Actor)
		if err != nil || !ok || card.Session != candidate.Session.Ref() {
			continue
		}
		h.ensurePaneRefresh(ctx, candidate.Actor, candidate.Session.Ref(), telegramMessage(card))
	}
}

func (h *Handler) scanBackgroundNotifications(
	ctx context.Context,
	panelFingerprints map[domain.UserID]string,
) {
	deliveries := h.service.BackgroundDeliveries()
	h.refreshBackgroundContexts(ctx, deliveries)
	byUser := make(map[domain.UserID][]application.BackgroundDelivery)
	for _, delivery := range deliveries {
		byUser[delivery.UserID] = append(byUser[delivery.UserID], delivery)
		if delivery.Notice.Notified {
			continue
		}
		delivered := !delivery.SendPush || h.sendBackgroundNotification(ctx, delivery)
		if delivered {
			h.markBackgroundNotified(ctx, delivery)
		}
	}
	for userID := range panelFingerprints {
		if _, ok := byUser[userID]; !ok {
			byUser[userID] = nil
		}
	}
	for _, userID := range h.service.BackgroundPanelUsers() {
		if _, ok := byUser[userID]; !ok {
			byUser[userID] = nil
		}
	}
	for userID, userDeliveries := range byUser {
		fingerprint := h.backgroundFingerprint(userDeliveries)
		previous, known := panelFingerprints[userID]
		if known && previous == fingerprint {
			continue
		}
		if h.refreshBackgroundPanel(ctx, userID) {
			panelFingerprints[userID] = fingerprint
		}
	}
}

func (h *Handler) sendBackgroundNotification(
	ctx context.Context,
	delivery application.BackgroundDelivery,
) bool {
	actor := application.Principal{UserID: delivery.UserID}
	copy := h.copy(actor)
	key := i18n.BackgroundFinishedNotice
	switch delivery.Notice.Kind {
	case domain.BackgroundError:
		key = i18n.BackgroundErrorNotice
	case domain.BackgroundNeedsAction:
		key = i18n.BackgroundActionNotice
	}
	name := delivery.Session.Name
	if name == "" {
		name = "…"
	}
	token, err := h.tokens.Session(
		delivery.UserID, telegramui.ActionSelectSession, delivery.Session.Ref(),
	)
	if err != nil {
		return false
	}
	label := name
	if delivery.Node.Name != "" {
		label += " · " + delivery.Node.Name
	}
	_, err = h.messenger.SendScreen(ctx, int64(delivery.UserID), telegramui.Screen{
		Name: telegramui.ScreenSessionCard,
		Text: copy.Format(key, label),
		Grid: telegramui.Grid{telegramui.Row{{
			Label: label,
			Callback: telegramui.Callback{
				Action: telegramui.ActionSelectSession, Token: token,
			},
		}}},
	})
	return err == nil
}

func (h *Handler) markBackgroundNotified(
	ctx context.Context,
	delivery application.BackgroundDelivery,
) {
	operation := fmt.Sprintf("background-notified-%d-%s-%d",
		delivery.UserID, delivery.Session.Ref().Key(), delivery.Notice.EventRevision)
	markCtx := application.WithOperationScope(ctx, operation)
	_ = h.service.MarkBackgroundNotified(
		markCtx, application.Principal{UserID: delivery.UserID},
		delivery.Session.Ref(), delivery.Notice.EventRevision,
	)
}

func (h *Handler) refreshBackgroundPanel(ctx context.Context, userID domain.UserID) bool {
	actor := application.Principal{UserID: userID}
	card, ok, err := h.service.TelegramResponseCard(actor)
	if err != nil || !ok {
		return true
	}
	session, err := h.service.ActiveSession(actor)
	if err != nil {
		return true
	}
	if card.Session != session.Ref() {
		return true
	}
	message := telegramMessage(card)
	page := h.rememberedCardPage(actor.UserID, message, session.Ref())
	if session.RuntimePhase != domain.RuntimeIdle {
		screen, renderErr := h.renderSessionCard(ctx, actor, session.Ref(), page)
		if renderErr != nil {
			return false
		}
		_, renderErr = h.editResponseCard(ctx, actor, message, screen)
		return renderErr == nil
	}
	snapshot, err := h.renderSessionCardSnapshot(ctx, actor, session.Ref(), page)
	if err != nil {
		return false
	}
	// Do not let the ordinary background-panel edit consume a completion.
	// Completion must replace the active carrier so Telegram surfaces it as a
	// new message; reconcileActiveFinalCards owns that transition.
	if finalAt, final := finalTranscriptAt(snapshot.events); final &&
		transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) &&
		!responseCardCoversFinal(card, session.Ref(), finalAt) {
		return false
	}
	_, err = h.editResponseCard(ctx, actor, message, snapshot.screen)
	return err == nil
}

func responseCardCoversFinal(
	card domain.TelegramResponseCard,
	ref domain.SessionRef,
	finalAt time.Time,
) bool {
	return card.Session == ref && !card.RenderedFinalAt.Before(finalAt)
}

func (h *Handler) settleRunningSessions(ctx context.Context) {
	if h.controls == nil {
		return
	}
	candidates := h.service.RunningSessions()
	semaphore := make(chan struct{}, backgroundSettleWorkers)
	var workers sync.WaitGroup
	for _, candidate := range candidates {
		candidate := candidate
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			events, err := h.controls.Transcript(
				ctx, candidate.Actor, candidate.Session.Ref(),
			)
			if err == nil {
				h.rememberCardTranscript(
					candidate.Session.Ref(), candidate.Session.Revision, events,
				)
				if h.settleFromTranscript(ctx, candidate.Actor, candidate.Session, events) {
					active, activeErr := h.service.ActiveSession(candidate.Actor)
					if activeErr == nil && active.Ref() == candidate.Session.Ref() {
						h.repostActiveFinal(ctx, candidate.Actor, candidate.Session.Ref())
					}
				}
			}
		}()
	}
	workers.Wait()
}

func (h *Handler) repostActiveFinal(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
) {
	card, ok, err := h.service.TelegramResponseCard(actor)
	if err != nil || !ok || card.Session != ref {
		return
	}
	screen, err := h.renderSessionCard(
		ctx, actor, ref, application.CardPageLatestResponseStart,
	)
	if err != nil {
		return
	}
	_, _ = h.repostFinalResponseCard(ctx, actor, telegramMessage(card), ref, screen)
}

func (h *Handler) backgroundFingerprint(deliveries []application.BackgroundDelivery) string {
	var result strings.Builder
	for _, delivery := range deliveries {
		percent, present := h.backgroundContextValue(delivery.Session.Ref())
		fmt.Fprintf(&result, "%s:%s:%d:%d:%t:%d;", delivery.Session.Ref().Key(),
			delivery.Notice.Kind, delivery.Notice.EventRevision,
			delivery.Notice.Acknowledgements, present, percent)
	}
	return result.String()
}
