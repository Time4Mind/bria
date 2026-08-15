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
	var reconciliation sync.WaitGroup
	reconciliation.Add(1)
	go func() {
		defer reconciliation.Done()
		h.runBackgroundReconciliation(ctx, interval)
	}()
	defer reconciliation.Wait()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if h.canRefresh() {
			h.scanBackgroundNotifications(ctx, panelFingerprints)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) runBackgroundReconciliation(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if h.canRefresh() {
			h.settleRunningSessions(ctx)
			h.restoreActivePaneRefreshes(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) restoreActivePaneRefreshes(ctx context.Context) {
	for _, candidate := range h.service.RunningSessions() {
		active, err := h.service.ActiveSession(candidate.Actor)
		if err != nil || active.Ref() != candidate.Session.Ref() {
			continue
		}
		card, ok, err := h.service.TelegramResponseCard(candidate.Actor)
		if err != nil || !ok {
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
		fingerprint := backgroundFingerprint(userDeliveries)
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
	screen, err := h.renderSessionCard(ctx, actor, session.Ref(), 0)
	if err != nil {
		return false
	}
	_, err = h.editResponseCard(ctx, actor, telegramMessage(card), screen)
	return err == nil
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
				if h.settleFromTranscript(ctx, candidate.Actor, candidate.Session, events) {
					active, activeErr := h.service.ActiveSession(candidate.Actor)
					if activeErr == nil && active.Ref() == candidate.Session.Ref() {
						h.refreshBackgroundPanel(ctx, candidate.Actor.UserID)
					}
				}
			}
		}()
	}
	workers.Wait()
}

func backgroundFingerprint(deliveries []application.BackgroundDelivery) string {
	var result strings.Builder
	for _, delivery := range deliveries {
		fmt.Fprintf(&result, "%s:%s:%d:%d;", delivery.Session.Ref().Key(),
			delivery.Notice.Kind, delivery.Notice.EventRevision,
			delivery.Notice.Acknowledgements)
	}
	return result.String()
}
