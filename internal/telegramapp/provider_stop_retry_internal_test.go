package telegramapp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerstop"
)

func TestProviderStopRetryOutcomeWhitelist(t *testing.T) {
	for _, outcome := range []string{
		"transcript_unavailable", "final_pending", "settlement_pending",
		"runtime_pending", "card_unavailable", "delivery_pending",
	} {
		if !providerStopRetriable(outcome) {
			t.Fatalf("temporary outcome %q is terminal", outcome)
		}
	}
	for _, outcome := range []string{
		"ignored", "superseded", "background_settled", "not_visible",
		"already_delivered", "delivered", "unknown",
	} {
		if providerStopRetriable(outcome) {
			t.Fatalf("terminal outcome %q is retried", outcome)
		}
	}
}

func TestProviderStopDuplicateWakesOneRetryFlight(t *testing.T) {
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	attempts := 0
	result := runProviderStopRetry(
		context.Background(), providerstop.Signal{}, time.Second,
		[]time.Duration{0, time.Hour}, func() bool { return true },
		wake, make(chan struct{}),
		func(context.Context, providerstop.Signal) (bool, string) {
			attempts++
			if attempts == 1 {
				return false, "final_pending"
			}
			return true, "delivered"
		},
	)
	if result.outcome != "delivered" || result.attempts != 2 || attempts != 2 {
		t.Fatalf("result=%#v attempts=%d", result, attempts)
	}
}

func TestProviderStopRetryHasOneSharedDeadline(t *testing.T) {
	startedAt := time.Now()
	result := runProviderStopRetry(
		context.Background(), providerstop.Signal{}, 30*time.Millisecond,
		[]time.Duration{0, time.Second}, func() bool { return true },
		make(chan struct{}), make(chan struct{}),
		func(ctx context.Context, _ providerstop.Signal) (bool, string) {
			<-ctx.Done()
			return false, "transcript_unavailable"
		},
	)
	if result.outcome != "watchdog_handoff" || result.reason != "deadline" ||
		result.attempts != 1 {
		t.Fatalf("result=%#v", result)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("shared deadline took %s", elapsed)
	}
}

func TestProviderStopTurnRegistryCoalescesAndSupersedes(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	session := domain.Session{
		ID: "session", NodeID: "node", ProviderSessionID: "provider",
		RuntimeGeneration: 4, LastEventAt: now,
		LastOperation: &domain.SessionOperationResult{
			OperationID: "turn-1", Action: domain.ActionSendInput,
		},
	}
	state := newProviderStopRetryState()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstID := providerStopTurnIdentityFor(session)
	first, started := state.startProviderStopTurn(firstID, now, firstCancel)
	if !started || first == nil {
		t.Fatal("first turn did not start")
	}
	_, duplicateCancel := context.WithCancel(context.Background())
	defer duplicateCancel()
	duplicate, duplicateStarted := state.startProviderStopTurn(
		firstID, now.Add(time.Millisecond), duplicateCancel,
	)
	if duplicateStarted || duplicate != first || first.duplicates != 1 {
		t.Fatalf("duplicate started=%v flight=%p first=%p duplicates=%d",
			duplicateStarted, duplicate, first, first.duplicates)
	}
	select {
	case <-first.wake:
	default:
		t.Fatal("duplicate did not wake existing flight")
	}

	session.LastOperation.OperationID = "turn-2"
	secondID := providerStopTurnIdentityFor(session)
	_, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	second, secondStarted := state.startProviderStopTurn(
		secondID, now.Add(2*time.Millisecond), secondCancel,
	)
	if !secondStarted || second == nil {
		t.Fatal("new turn did not supersede old flight")
	}
	select {
	case <-first.superseded:
	case <-time.After(time.Second):
		t.Fatal("old flight was not marked superseded")
	}
	select {
	case <-firstCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("old flight context was not cancelled")
	}
	if duplicates := state.finishProviderStopTurn(firstID.key); duplicates != 1 {
		t.Fatalf("first duplicates=%d", duplicates)
	}
	state.finishProviderStopTurn(secondID.key)
}

func TestProviderStopTurnRegistryEvictsAlreadySupersededFlight(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	state := newProviderStopRetryState()
	session := domain.Session{
		ID: "session", NodeID: "node", ProviderSessionID: "provider",
		RuntimeGeneration: 4, LastEventAt: now,
		LastOperation: &domain.SessionOperationResult{
			Action: domain.ActionSendInput,
		},
	}
	for index := 0; index < maxRememberedProviderStopTurns+2; index++ {
		session.LastOperation.OperationID = fmt.Sprintf("turn-%d", index)
		identity := providerStopTurnIdentityFor(session)
		_, cancel := context.WithCancel(context.Background())
		if _, started := state.startProviderStopTurn(
			identity, now.Add(time.Duration(index)*time.Millisecond), cancel,
		); !started {
			t.Fatalf("turn %d did not start", index)
		}
	}
	if len(state.turns) != maxRememberedProviderStopTurns {
		t.Fatalf("bounded registry size=%d", len(state.turns))
	}
}
