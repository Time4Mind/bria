// Package inputcarrierguard waits for the Telegram carrier created by one
// durable user input without mutating session or transport state.
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

// Await prevents an input status from editing the previous Telegram message
// while the input's new carrier is still being sent. A newer presentation
// supersedes the status without transport.
func Await(ctx context.Context, store Store, sessionID domain.SessionID, operationID string, stored telegramstate.Card) (telegramstate.Card, bool, error) {
	expected, matched := presentationOperation(operationID)
	if !matched || stored.LastPresentationOperation == expected {
		return stored, true, nil
	}
	initialRevision := stored.CarrierRevision
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(waitLimit)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return telegramstate.Card{}, false, ctx.Err()
		case <-timer.C:
			return stored, false, nil
		case <-ticker.C:
			state, err := store.Load(ctx)
			if err != nil {
				return telegramstate.Card{}, false, err
			}
			current, ok := state.Card(sessionID)
			if !ok || current.Carrier.ChatID <= 0 || current.Carrier.MessageID <= 0 {
				return telegramstate.Card{}, false, nil
			}
			if current.LastPresentationOperation == expected {
				return current, true, nil
			}
			if current.CarrierRevision > initialRevision {
				return current, false, nil
			}
			stored = current
		}
	}
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
