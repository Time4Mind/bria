package storage

import (
	"context"
	"fmt"

	"bria/internal/domain"
)

// SetModelPreferences atomically persists next-turn choices only while Ready.
// Reloading under the shared path lock prevents racing lifecycle transitions.
func (store *SessionStore) SetModelPreferences(ctx context.Context, id domain.SessionID, model, effort string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.reload(); err != nil {
		return err
	}
	intent, ok := store.byID[id]
	if !ok {
		return ErrSessionNotFound
	}
	current := store.byIntent[intent]
	next, err := current.WithModelPreferences(model, effort)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvariantConflict, err)
	}
	if next.Equal(current) {
		return nil
	}
	sessions := cloneSessions(store.byIntent)
	sessions[intent] = next
	if err := store.persist(sessions, store.checkpoint, store.telegramUI); err != nil {
		_ = store.reload()
		return fmt.Errorf("persist model preferences: %w", err)
	}
	store.byIntent = sessions
	return nil
}

// ProviderModelPreferences returns persisted values; empty means CLI default.
func (store *SessionStore) ProviderModelPreferences(ctx context.Context, id domain.SessionID) (string, string, error) {
	session, err := store.Load(ctx, id)
	if err != nil {
		return "", "", err
	}
	snapshot := session.Snapshot()
	return snapshot.Model, snapshot.Effort, nil
}
