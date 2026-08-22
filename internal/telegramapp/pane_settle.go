package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

func (h *Handler) settleFromTranscript(
	ctx context.Context,
	actor application.Principal,
	session domain.Session,
	events []transcript.Event,
) bool {
	if session.RuntimePhase != domain.RuntimeRunning {
		return false
	}
	turn, ok := transcript.LatestCompletedTurn(events, transcript.Backend(session.Backend))
	if !ok {
		return false
	}
	final := turn.Final
	finalAt := turn.FinalAt
	if turn.HasUser && turn.UserAt.Before(session.LastEventAt) {
		return false
	}
	if !transcriptFinalBelongsToCurrentTurn(session, finalAt, time.Now()) {
		return false
	}
	settleCtx := application.WithOperationScope(
		ctx, fmt.Sprintf("transcript-final-%s-%d", session.Ref().Key(), finalAt.UnixNano()),
	)
	phase := domain.RuntimeIdle
	var result *domain.SessionOperationResult
	if final.Error {
		phase = domain.RuntimeDegraded
		result = &domain.SessionOperationResult{
			OperationID: fmt.Sprintf("transcript-error-%d", finalAt.UnixNano()),
			Action:      domain.ActionSendInput, Status: domain.OperationFailed,
			Detail: boundedOperationDetail(final.Text),
		}
	}
	if h.service.PublishSessionRuntime(settleCtx, session, phase, result) != nil {
		return false
	}
	if !final.Error {
		go h.deliverFinalFiles(ctx, actor, session.Ref(), events)
	}
	return true
}

func boundedOperationDetail(value string) string {
	value = strings.TrimSpace(value)
	for len(value) > 512 {
		_, size := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-size]
	}
	return value
}

const deliveredInputTimestampSkew = 2 * time.Minute
const deliveredInputSettleGrace = 2 * time.Second

func transcriptFinalBelongsToCurrentTurn(
	session domain.Session,
	finalAt time.Time,
	now time.Time,
) bool {
	if !finalAt.Before(session.LastEventAt) {
		return true
	}
	operation := session.LastOperation
	if operation != nil && operation.Action == domain.ActionSendInput &&
		operation.Status == domain.OperationQueued &&
		session.RuntimePhase == domain.RuntimeIdle {
		// Transcript settlement and Telegram delivery are separate durable
		// steps. If the process exits after publishing idle but before reposting
		// the card, LastEventAt contains the settlement time while LastOperation
		// still contains the queued prompt. The operation timestamp is the
		// durable turn boundary: a final after it belongs to that prompt, while
		// a previous turn's final does not.
		return !finalAt.Before(operation.At) &&
			session.LastEventAt.Sub(finalAt) <= deliveredInputTimestampSkew
	}
	// Older builds stamped LastEventAt again when the local node acknowledged
	// an already-delivered prompt. A fast backend can finish before that
	// acknowledgement is replicated. Accept only that narrow legacy skew; all
	// other finals older than the current turn remain stale.
	return operation != nil && operation.Action == domain.ActionSendInput &&
		operation.Status == domain.OperationSucceeded &&
		operation.Detail != runtimehost.ProviderConfirmationPendingDetail &&
		session.LastEventAt.Sub(finalAt) <= deliveredInputTimestampSkew &&
		!now.Before(operation.At.Add(deliveredInputSettleGrace))
}

func (h *Handler) currentPaneGeneration(userID domain.UserID, generation uint64) bool {
	h.paneMu.Lock()
	defer h.paneMu.Unlock()
	return h.paneGeneration[userID] == generation
}

func (h *Handler) cancelPaneRefresh(userID domain.UserID) {
	h.paneMu.Lock()
	cancel := h.paneCancels[userID]
	h.paneGeneration[userID]++
	delete(h.paneWorkers, userID)
	delete(h.paneWorkerRefs, userID)
	delete(h.paneCancels, userID)
	h.paneMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func waitPaneRefresh(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
