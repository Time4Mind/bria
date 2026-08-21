package telegramapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const backgroundSettleWorkers = 8

const activeFinalReconcileFallback = 5 * time.Second

type activeFinalReconcileSchedule map[string]time.Time

var backgroundTranscriptBudget = 3 * time.Second

type settledCardCheck struct {
	messageID       int64
	session         domain.SessionRef
	sessionRevision uint64
	renderedFinalAt time.Time
}

func (h *Handler) RunBackgroundNotifications(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 1200 * time.Millisecond
	}
	panelFingerprints := make(map[domain.UserID]string)
	settlementSchedule := make(backgroundSettlementSchedule)
	finalReconcileSchedule := make(activeFinalReconcileSchedule)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if h.canRefresh() {
			// Keep runtime settlement and card delivery in one ordered pass. Two
			// independent loops can observe the same revision in opposite order,
			// allowing a routine panel edit to consume a just-finished turn before
			// the completion carrier is reposted.
			h.settleDueRunningSessions(ctx, time.Now(), interval, settlementSchedule)
			h.reconcileActiveFinalCards(ctx, time.Now(), finalReconcileSchedule)
			h.restoreActivePaneRefreshes(ctx)
			h.restoreClusterUpdateRefreshes(ctx)
			h.scanBackgroundNotifications(ctx, panelFingerprints)
			h.flushTranscriptTriggerGaps(time.Now())
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
func (h *Handler) reconcileActiveFinalCards(
	ctx context.Context,
	now time.Time,
	schedule activeFinalReconcileSchedule,
) {
	if h.controls == nil {
		return
	}
	candidates := h.service.ActiveSessions()
	current := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		key := backgroundSettlementKey(candidate)
		current[key] = true
		if next := schedule[key]; !next.IsZero() && now.Before(next) {
			continue
		}
		startedAt := time.Now()
		func() {
			defer func() {
				completedAt := now.Add(time.Since(startedAt))
				schedule[key] = completedAt.Add(activeFinalReconcileFallback)
			}()
			actor := candidate.Actor
			session := candidate.Session
			if !runtimeCanDeliverFinal(session) {
				return
			}
			card, ok, err := h.service.TelegramResponseCard(actor)
			if err != nil {
				return
			}
			if !finalMaySurfaceOverView(
				h.visibleCardSnapshot(actor.UserID), card, ok, session.Ref(),
			) {
				return
			}
			if ok && card.Session == session.Ref() &&
				h.validatedSettledCard(actor.UserID, card, session) {
				return
			}
			snapshot, err := h.renderSessionCardSnapshot(
				ctx, actor, session.Ref(), application.CardPageLatestResponseStart,
			)
			if err != nil {
				return
			}
			h.observeTranscriptWatchdog(session, snapshot.events, "card_reconcile", time.Now())
			finalAt, final := finalTranscriptAt(snapshot.events)
			if !final || !transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
				return
			}
			if ok && responseCardCoversFinal(card, session.Ref(), finalAt) {
				h.rememberValidatedSettledCard(actor.UserID, card, session)
				return
			}
			done, _ := h.deliverActiveFinalScreen(
				ctx, actor, session, finalAt, snapshot.screen,
			)
			if !done {
				return
			}
			card, ok, err = h.service.TelegramResponseCard(actor)
			if err == nil && ok && responseCardCoversFinal(card, session.Ref(), finalAt) {
				h.rememberValidatedSettledCard(actor.UserID, card, session)
			}
		}()
	}
	for key := range schedule {
		if !current[key] {
			delete(schedule, key)
		}
	}
}

func (h *Handler) validatedSettledCard(
	userID domain.UserID,
	card domain.TelegramResponseCard,
	session domain.Session,
) bool {
	h.cardDataMu.RLock()
	check, ok := h.settledCards[userID]
	h.cardDataMu.RUnlock()
	return ok && check == (settledCardCheck{
		messageID: card.MessageID, session: card.Session,
		sessionRevision: session.Revision, renderedFinalAt: card.RenderedFinalAt,
	})
}

func (h *Handler) rememberValidatedSettledCard(
	userID domain.UserID,
	card domain.TelegramResponseCard,
	session domain.Session,
) {
	h.cardDataMu.Lock()
	h.settledCards[userID] = settledCardCheck{
		messageID: card.MessageID, session: card.Session,
		sessionRevision: session.Revision, renderedFinalAt: card.RenderedFinalAt,
	}
	h.cardDataMu.Unlock()
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
	page := h.rememberedCardPage(actor.UserID, session.Ref())
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
