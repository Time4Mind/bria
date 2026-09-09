package storage

import (
	"context"
	"errors"

	"bria/internal/cardeventhistory"
	"bria/internal/domain"
)

// InsertCardRuntimeEvent commits identity and content in the same state write.
func (store *SessionStore) InsertCardRuntimeEvent(ctx context.Context, id domain.SessionID, promptID, eventID, item, kind string) error {
	return historyError(cardeventhistory.Insert(ctx, store, id, promptID, eventID, item, kind))
}

func (store *SessionStore) InsertCardTypedHistoryAfterPrompt(ctx context.Context, id domain.SessionID, promptID, item, kind string) error {
	return historyError(cardeventhistory.InsertTyped(ctx, store, id, promptID, item, kind))
}

func historyError(err error) error {
	if errors.Is(err, cardeventhistory.ErrCardUnavailable) {
		return ErrSessionNotFound
	}
	return err
}
