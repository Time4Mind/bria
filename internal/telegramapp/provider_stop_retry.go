package telegramapp

import (
	"context"
	"fmt"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerstop"
)

const (
	maxRememberedProviderStopTurns = 512
	providerStopTurnTTL            = 10 * time.Minute
)

var providerStopRetryDeadline = 3 * time.Second

type providerStopRetryResult struct {
	outcome  string
	reason   string
	last     string
	attempts int
	log      bool
}

type providerStopTurnIdentity struct {
	key        string
	refKey     string
	operation  string
	generation uint64
}

type providerStopFlight struct {
	startedAt  time.Time
	wake       chan struct{}
	superseded chan struct{}
	cancel     context.CancelFunc
	running    bool
	duplicates int
}

func providerStopTurnIdentityFor(session domain.Session) providerStopTurnIdentity {
	turn := ""
	if operation := session.LastOperation; operation != nil &&
		operation.Action == domain.ActionSendInput {
		turn = operation.OperationID
	}
	operation := turn
	if turn == "" {
		turn = fmt.Sprintf("event:%d:%d", session.LastEventAt.UnixNano(), session.Revision)
		operation = "legacy"
	}
	refKey := session.Ref().Key()
	return providerStopTurnIdentity{
		key: fmt.Sprintf("%s\x00%d\x00%s\x00%s", refKey,
			session.RuntimeGeneration, session.ProviderSessionID, turn),
		refKey: refKey, operation: operation, generation: session.RuntimeGeneration,
	}
}

func (s *providerStopRetryState) startProviderStopTurn(
	identity providerStopTurnIdentity,
	now time.Time,
	cancel context.CancelFunc,
) (*providerStopFlight, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns == nil {
		s.turns = make(map[string]*providerStopFlight)
	}
	if s.activeByRef == nil {
		s.activeByRef = make(map[string]string)
	}
	cutoff := now.Add(-providerStopTurnTTL)
	for existing, flight := range s.turns {
		if !flight.running && flight.startedAt.Before(cutoff) {
			delete(s.turns, existing)
		}
	}
	if flight, exists := s.turns[identity.key]; exists {
		if flight.running {
			flight.duplicates++
			select {
			case flight.wake <- struct{}{}:
			default:
			}
		}
		return flight, false
	}
	if oldKey := s.activeByRef[identity.refKey]; oldKey != "" && oldKey != identity.key {
		if old := s.turns[oldKey]; old != nil && old.running {
			supersedeProviderStopFlight(old)
		}
	}
	for len(s.turns) >= maxRememberedProviderStopTurns {
		oldestKey := ""
		var oldestAt time.Time
		for existing, flight := range s.turns {
			if oldestKey == "" || flight.startedAt.Before(oldestAt) {
				oldestKey, oldestAt = existing, flight.startedAt
			}
		}
		oldest := s.turns[oldestKey]
		if oldest.running {
			supersedeProviderStopFlight(oldest)
		}
		for refKey, activeKey := range s.activeByRef {
			if activeKey == oldestKey {
				delete(s.activeByRef, refKey)
				break
			}
		}
		delete(s.turns, oldestKey)
	}
	flight := &providerStopFlight{
		startedAt: now, wake: make(chan struct{}, 1), superseded: make(chan struct{}),
		cancel: cancel, running: true,
	}
	s.turns[identity.key] = flight
	s.activeByRef[identity.refKey] = identity.key
	return flight, true
}

func supersedeProviderStopFlight(flight *providerStopFlight) {
	if flight == nil {
		return
	}
	select {
	case <-flight.superseded:
	default:
		close(flight.superseded)
	}
	flight.cancel()
}

func (s *providerStopRetryState) finishProviderStopTurn(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	flight := s.turns[key]
	if flight == nil {
		return 0
	}
	flight.running = false
	for refKey, activeKey := range s.activeByRef {
		if activeKey == key {
			delete(s.activeByRef, refKey)
			break
		}
	}
	return flight.duplicates
}

func providerStopRetriable(outcome string) bool {
	switch outcome {
	case "transcript_unavailable", "final_pending", "settlement_pending",
		"runtime_pending", "card_unavailable", "delivery_pending":
		return true
	default:
		return false
	}
}

func stoppedProviderStopRetry(
	ctx context.Context,
	retryCtx context.Context,
	superseded <-chan struct{},
	last string,
	attempts int,
) providerStopRetryResult {
	select {
	case <-superseded:
		return providerStopRetryResult{
			outcome: "superseded", reason: "turn_changed", last: last,
			attempts: attempts, log: true,
		}
	default:
	}
	if retryCtx.Err() == context.DeadlineExceeded {
		return providerStopRetryResult{
			outcome: "watchdog_handoff", reason: "deadline", last: last,
			attempts: attempts, log: true,
		}
	}
	if ctx.Err() != nil {
		return providerStopRetryResult{
			outcome: "cancelled", reason: "context", last: last, attempts: attempts,
		}
	}
	return providerStopRetryResult{
		outcome: "watchdog_handoff", reason: "cancelled", last: last,
		attempts: attempts, log: true,
	}
}

func runProviderStopRetry(
	ctx context.Context,
	signal providerstop.Signal,
	deadline time.Duration,
	delays []time.Duration,
	canRefresh func() bool,
	wake <-chan struct{},
	superseded <-chan struct{},
	attempt func(context.Context, providerstop.Signal) (bool, string),
) providerStopRetryResult {
	if deadline <= 0 || len(delays) == 0 || canRefresh == nil || attempt == nil {
		return providerStopRetryResult{
			outcome: "watchdog_handoff", reason: "invalid_policy", log: true,
		}
	}
	retryCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	last := "not_attempted"
	for index, delay := range delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return stoppedProviderStopRetry(ctx, retryCtx, superseded, last, index)
			case <-retryCtx.Done():
				timer.Stop()
				return stoppedProviderStopRetry(ctx, retryCtx, superseded, last, index)
			case <-superseded:
				timer.Stop()
				return stoppedProviderStopRetry(ctx, retryCtx, superseded, last, index)
			case <-wake:
				timer.Stop()
			case <-timer.C:
			}
		}
		if !canRefresh() {
			return providerStopRetryResult{
				outcome: "watchdog_handoff", reason: "leadership_lost", last: last,
				attempts: index, log: true,
			}
		}
		done, outcome := attempt(retryCtx, signal)
		last = outcome
		attempts := index + 1
		if done {
			reason := "terminal"
			if outcome == "superseded" {
				reason = "turn_changed"
			}
			return providerStopRetryResult{
				outcome: outcome, reason: reason, last: outcome,
				attempts: attempts, log: true,
			}
		}
		if !providerStopRetriable(outcome) {
			return providerStopRetryResult{
				outcome: "watchdog_handoff", reason: "non_retryable", last: outcome,
				attempts: attempts, log: true,
			}
		}
		if retryCtx.Err() != nil {
			return stoppedProviderStopRetry(ctx, retryCtx, superseded, outcome, attempts)
		}
	}
	return providerStopRetryResult{
		outcome: "watchdog_handoff", reason: "attempts_exhausted", last: last,
		attempts: len(delays), log: true,
	}
}
