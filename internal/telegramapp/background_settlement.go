package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/application"
)

const backgroundSettlementFallback = 5 * time.Second

type backgroundSettlementSchedule map[string]time.Time

type backgroundSettlementResult struct {
	key       string
	readOK    bool
	retrySoon bool
	completed time.Time
}

func backgroundSettlementKey(candidate application.RunningSession) string {
	return fmt.Sprintf("%s\x00%d\x00%s", candidate.Session.Ref().Key(),
		candidate.Session.RuntimeGeneration, candidate.Session.ProviderSessionID)
}

func (h *Handler) settleDueRunningSessions(
	ctx context.Context,
	now time.Time,
	retryInterval time.Duration,
	schedule backgroundSettlementSchedule,
) {
	if h.controls.transcript == nil {
		return
	}
	if retryInterval <= 0 {
		retryInterval = 1200 * time.Millisecond
	}
	candidates := h.service.RunningSessions()
	current := make(map[string]bool, len(candidates))
	due := make([]application.RunningSession, 0, len(candidates))
	for _, candidate := range candidates {
		key := backgroundSettlementKey(candidate)
		current[key] = true
		if h.hasPaneRefresh(candidate.Actor.UserID, candidate.Session.Ref()) {
			delete(schedule, key)
			continue
		}
		if next := schedule[key]; !next.IsZero() && now.Before(next) {
			continue
		}
		due = append(due, candidate)
	}
	for key := range schedule {
		if !current[key] {
			delete(schedule, key)
		}
	}
	if len(due) == 0 {
		return
	}
	semaphore := make(chan struct{}, backgroundSettleWorkers)
	results := make(chan backgroundSettlementResult, len(due))
	var workers sync.WaitGroup
	for _, candidate := range due {
		candidate := candidate
		workers.Add(1)
		go func() {
			defer workers.Done()
			startedAt := time.Now()
			result := backgroundSettlementResult{key: backgroundSettlementKey(candidate)}
			defer func() {
				result.completed = now.Add(time.Since(startedAt))
				results <- result
			}()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			events, err := h.readBackgroundTranscript(ctx, candidate.Actor, candidate.Session.Ref())
			if err == nil {
				observedAt := now.Add(time.Since(startedAt))
				result.readOK = true
				h.observeTranscriptWatchdog(candidate.Session, events, "background", observedAt)
				h.rememberCardTranscript(
					candidate.Session.Ref(), candidate.Session.Revision,
					candidate.Session.ProviderSessionID, events,
				)
				settled := h.settleFromTranscript(ctx, candidate.Actor, candidate.Session, events)
				if finalAt, final := finalTranscriptAt(events); final &&
					transcriptFinalBelongsToCurrentTurn(candidate.Session, finalAt, observedAt) && !settled {
					result.retrySoon = true
				}
				if settled {
					active, activeErr := h.service.ActiveSession(candidate.Actor)
					if activeErr == nil && active.Ref() == candidate.Session.Ref() {
						if finalAt, final := finalTranscriptAt(events); final {
							_, _ = h.deliverActiveFinal(
								ctx, candidate.Actor, active, finalAt,
							)
						}
					}
				}
			}
		}()
	}
	workers.Wait()
	close(results)
	for result := range results {
		schedule[result.key] = nextBackgroundSettlement(result, now, retryInterval)
	}
}

func nextBackgroundSettlement(
	result backgroundSettlementResult,
	fallbackStart time.Time,
	retryInterval time.Duration,
) time.Time {
	completed := result.completed
	if completed.IsZero() {
		completed = fallbackStart
	}
	delay := backgroundSettlementFallback
	if !result.readOK || result.retrySoon {
		delay = retryInterval
	}
	return completed.Add(delay)
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
