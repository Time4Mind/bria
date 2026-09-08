package storage

import (
	"context"
	"errors"

	"bria/internal/cardhistory"
	"bria/internal/domain"
	"bria/internal/telegramstate"
)

// RestoreAcceptedFinalForSession saves only while the exact session still owns
// this final. Stale sessions return false without rewriting the state file.
func (store *SessionStore) RestoreAcceptedFinalForSession(ctx context.Context, expected domain.Session, messageID, final string) (bool, error) {
	stale := errors.New("bound final session is stale")
	err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		// UpdateTelegramUI holds the shared store mutex after reloading sessions.
		current, ok := store.byIntent[expected.IntentID()]
		if !ok || !current.Equal(expected) {
			return stale
		}
		card, ok := state.Cards[expected.ID()]
		if !ok {
			return ErrSessionNotFound
		}
		if err := cardhistory.RestoreFinal(&card, messageID, final); err != nil {
			return err
		}
		return state.SetCard(card)
	})
	if err == stale {
		return false, nil
	}
	return err == nil, err
}
