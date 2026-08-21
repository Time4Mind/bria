package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/providerstop"
)

var providerStopRetryDelays = []time.Duration{
	0,
	50 * time.Millisecond,
	150 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
	1500 * time.Millisecond,
}

// RunProviderStopNotifications preempts the periodic live-card cadence. The
// provider hook is only a wake-up hint: every attempt rereads the canonical
// transcript and requires an assistant final for the current Bria turn.
func (h *Handler) RunProviderStopNotifications(
	ctx context.Context,
	signals <-chan providerstop.Signal,
) {
	if signals == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case signal, ok := <-signals:
			if !ok {
				return
			}
			go h.handleProviderStopWithRetry(ctx, signal)
		}
	}
}

func (h *Handler) handleProviderStopWithRetry(ctx context.Context, signal providerstop.Signal) {
	startedAt := time.Now()
	lastOutcome := "not_attempted"
	for attempt, delay := range providerStopRetryDelays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if !h.canRefresh() {
			return
		}
		done, outcome := h.handleProviderStop(ctx, signal)
		lastOutcome = outcome
		if done {
			processlog.Detailf(
				"bria telegram: provider_stop ref=%q outcome=%s attempts=%d duration_ms=%d",
				signal.NodeID+"/"+signal.SessionID, outcome, attempt+1,
				time.Since(startedAt).Milliseconds(),
			)
			return
		}
	}
	processlog.Servicef(
		"bria telegram: provider_stop ref=%q outcome=retry_exhausted last=%s attempts=%d duration_ms=%d",
		signal.NodeID+"/"+signal.SessionID, lastOutcome, len(providerStopRetryDelays),
		time.Since(startedAt).Milliseconds(),
	)
}

func (h *Handler) handleProviderStop(
	ctx context.Context,
	signal providerstop.Signal,
) (bool, string) {
	actor, session, ok := h.providerStopSession(signal)
	if !ok {
		return true, "ignored" // stale, foreign, or already replaced session
	}
	events, err := h.readBackgroundTranscript(ctx, actor, session.Ref())
	if err != nil {
		return false, "transcript_unavailable"
	}
	h.confirmTranscriptTrigger(session, events)
	h.rememberCardTranscript(
		session.Ref(), session.Revision, session.ProviderSessionID, events,
	)
	finalAt, final := finalTranscriptAt(events)
	if !final || !transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
		return false, "final_pending"
	}
	if session.RuntimePhase == domain.RuntimeRunning &&
		!h.settleFromTranscript(ctx, actor, session, events) {
		// A node heartbeat can publish the same transcript final between the
		// snapshot above and our command. Treat that stale-write race as success
		// once the replicated runtime has already reached a settled phase.
		latest, latestErr := h.service.Session(actor, session.Ref())
		if latestErr != nil || (latest.RuntimePhase != domain.RuntimeIdle &&
			latest.RuntimePhase != domain.RuntimeDegraded) {
			return false, "settlement_pending"
		}
	}
	latest, err := h.service.Session(actor, session.Ref())
	if err != nil || (latest.RuntimePhase != domain.RuntimeIdle &&
		latest.RuntimePhase != domain.RuntimeDegraded) {
		return false, "runtime_pending"
	}
	return h.deliverActiveFinal(ctx, actor, latest, finalAt)
}

func (h *Handler) providerStopSession(
	signal providerstop.Signal,
) (application.Principal, domain.Session, bool) {
	if signal.Validate() != nil {
		return application.Principal{}, domain.Session{}, false
	}
	ref := domain.SessionRef{
		NodeID: domain.NodeID(signal.NodeID), SessionID: domain.SessionID(signal.SessionID),
	}
	return h.service.ProviderSession(
		ref, signal.ProviderSessionID, signal.RuntimeGeneration,
	)
}
