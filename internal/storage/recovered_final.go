package storage

import (
	"bria/internal/cardhistory"
	"bria/internal/domain"
	"bria/internal/telegramstate"
	"context"
	"time"
)

// RestoreAcceptedFinal is atomic and idempotent at the exact prompt anchor.
func (store *SessionStore) RestoreAcceptedFinal(ctx context.Context, id domain.SessionID, messageID, final string) error {
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[id]
		if !ok {
			return ErrSessionNotFound
		}
		beforeHistory, beforePending := len(card.History), len(card.PendingFinalOperations)
		if err := cardhistory.RestoreFinal(&card, messageID, final); err != nil {
			return err
		}
		if len(card.History) != beforeHistory || len(card.PendingFinalOperations) != beforePending {
			card.TouchEvent(time.Now())
		}
		return state.SetCard(card)
	})
}
