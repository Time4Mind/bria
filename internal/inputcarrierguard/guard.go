// Package inputcarrierguard waits for one durable input's Telegram carrier.
package inputcarrierguard

import (
	"context"
	"strconv"
	"strings"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

const waitLimit = 10 * time.Second

type Store interface {
	Load(context.Context) (telegramstate.State, error)
}
type Outcome string

const (
	OutcomeUnscoped   Outcome = "unscoped"
	OutcomeMatched    Outcome = "matched"
	OutcomeSuperseded Outcome = "superseded"
	OutcomeTimeout    Outcome = "timeout"
)

type Result struct {
	Card    telegramstate.Card
	Ready   bool
	Outcome Outcome
}

func Await(ctx context.Context, store Store, sessionID domain.SessionID, operationID string, stored telegramstate.Card) (Result, error) {
	return await(ctx, store, sessionID, operationID, stored, waitLimit, 10*time.Millisecond)
}

func await(ctx context.Context, store Store, sessionID domain.SessionID, operationID string, stored telegramstate.Card, limit, poll time.Duration) (Result, error) {
	expected, matched := presentationOperation(operationID)
	if !matched {
		return Result{Card: stored, Ready: true, Outcome: OutcomeUnscoped}, nil
	}
	if sameInputCarrier(stored.CarrierOwner(), expected) {
		return Result{Card: stored, Ready: true, Outcome: OutcomeMatched}, nil
	}
	initialRevision := stored.CarrierRevision
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-timer.C:
			return Result{Card: stored, Outcome: OutcomeTimeout}, nil
		case <-ticker.C:
			state, err := store.Load(ctx)
			if err != nil {
				return Result{}, err
			}
			current, ok := state.Card(sessionID)
			if !ok || current.Carrier.ChatID <= 0 || current.Carrier.MessageID <= 0 {
				return Result{Card: current, Outcome: OutcomeSuperseded}, nil
			}
			if sameInputCarrier(current.CarrierOwner(), expected) {
				return Result{Card: current, Ready: true, Outcome: OutcomeMatched}, nil
			}
			if current.CarrierRevision > initialRevision {
				return Result{Card: current, Outcome: OutcomeSuperseded}, nil
			}
		}
	}
}

func sameInputCarrier(operation, expected string) bool {
	if operation == expected {
		return true
	}
	canonical, matched := presentationOperation(operation)
	return matched && canonical == expected
}

func presentationOperation(operationID string) (string, bool) {
	const prefix, marker = "telegram-update:", ":prompt-status:"
	if !strings.HasPrefix(operationID, prefix) {
		return "", false
	}
	end := strings.Index(operationID[len(prefix):], marker)
	if end < 0 {
		return "", false
	}
	updateID := operationID[len(prefix) : len(prefix)+end]
	parsed, err := strconv.ParseInt(updateID, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != updateID {
		return "", false
	}
	return "status:" + updateID, true
}
