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
			h.scheduleProviderStop(ctx, signal)
		}
	}
}

func (h *Handler) scheduleProviderStop(ctx context.Context, signal providerstop.Signal) {
	startedAt := time.Now()
	_, session, ok := h.providerStopSession(signal)
	if !ok {
		processlog.Outcomef(processlog.Detail, "ignored",
			"bria telegram: provider_stop ref=%q generation=%d outcome=ignored reason=stale_or_replaced attempts=0 duration_ms=%d",
			signal.NodeID+"/"+signal.SessionID, signal.RuntimeGeneration,
			time.Since(startedAt).Milliseconds(),
		)
		return
	}
	identity := providerStopTurnIdentityFor(session)
	flightCtx, cancel := context.WithCancel(ctx)
	flight, started := h.startProviderStopTurn(identity, startedAt, cancel)
	if !started {
		cancel()
		return
	}
	go h.handleProviderStopWithRetry(flightCtx, signal, identity, flight)
}

func (h *Handler) handleProviderStopWithRetry(
	ctx context.Context,
	signal providerstop.Signal,
	identity providerStopTurnIdentity,
	flight *providerStopFlight,
) {
	startedAt := flight.startedAt
	defer flight.cancel()
	result := runProviderStopRetry(
		ctx, signal, providerStopRetryDeadline, providerStopRetryDelays,
		h.canRefresh, flight.wake, flight.superseded,
		func(attemptCtx context.Context, attemptSignal providerstop.Signal) (bool, string) {
			_, current, ok := h.providerStopSession(attemptSignal)
			if !ok {
				return true, "ignored"
			}
			if providerStopTurnIdentityFor(current).key != identity.key {
				return true, "superseded"
			}
			return h.handleProviderStop(attemptCtx, attemptSignal)
		},
	)
	duplicates := h.finishProviderStopTurn(identity.key)
	if !result.log {
		return
	}
	format := "bria telegram: provider_stop ref=%q generation=%d operation=%q outcome=%s reason=%s last=%s attempts=%d duplicates=%d duration_ms=%d"
	arguments := []any{
		signal.NodeID + "/" + signal.SessionID, identity.generation, identity.operation,
		result.outcome, result.reason, result.last, result.attempts, duplicates,
		time.Since(startedAt).Milliseconds(),
	}
	if result.outcome == "watchdog_handoff" {
		processlog.Outcomef(processlog.Service, result.outcome, format, arguments...)
		return
	}
	processlog.Outcomef(processlog.Detail, result.outcome, format, arguments...)
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
