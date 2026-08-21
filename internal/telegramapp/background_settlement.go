package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

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
			events, err := h.readBackgroundTranscript(ctx, candidate.Actor, candidate.Session.Ref())
			if err == nil {
				h.rememberCardTranscript(
					candidate.Session.Ref(), candidate.Session.Revision,
					candidate.Session.ProviderSessionID, events,
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
