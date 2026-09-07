package storage

import (
	"context"
	"fmt"

	"bria/internal/domain"
)

// HasEmptyCloseEligibility is advisory for confirmation text. The destructive
// operation always rechecks the evidence and lifecycle under the same lock.
func (store *SessionStore) HasEmptyCloseEligibility(ctx context.Context, id domain.SessionID) (bool, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return false, err
	}
	card, ok := state.Cards[id]
	return ok && card.EmptyCloseEligible && len(card.History) == 0, nil
}

// DeleteEmptyClosing must only be called after the exact provider process exit
// has been confirmed. Unknown or any recorded content means retain/archive.
// Session, UI card, and selection references are removed in one durable write.
func (store *SessionStore) DeleteEmptyClosing(ctx context.Context, expected domain.Session) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if expected.Status() != domain.SessionClosing {
		return false, fmt.Errorf("empty deletion requires closing session")
	}
	return store.deleteProvenEmpty(ctx, expected)
}

// DeleteEmptyFailedStart deletes only a locally created, empty initial start
// failure. The SessionStarter error contract guarantees no adapter remains;
// this is not a way to delete an unbound session with unknown provenance.
func (store *SessionStore) DeleteEmptyFailedStart(ctx context.Context, expected domain.Session) (bool, error) {
	target, recovering := expected.RecoveryTarget()
	_, bound := expected.Binding()
	if expected.Status() != domain.SessionAwaitingRecovery || !recovering || target != domain.SessionStarting || bound {
		return false, fmt.Errorf("empty failed-start deletion requires unbound initial recovery")
	}
	return store.deleteProvenEmpty(ctx, expected)
}

// DeleteEmptyAwaitingRecovery removes an empty session whose provider could
// not be resumed. It is used during startup reconciliation after the caller
// has stopped (or confirmed absent) the retained provider process.
func (store *SessionStore) DeleteEmptyAwaitingRecovery(ctx context.Context, expected domain.Session) (bool, error) {
	if expected.Status() != domain.SessionAwaitingRecovery {
		return false, fmt.Errorf("empty recovery deletion requires awaiting recovery")
	}
	return store.deleteProvenEmpty(ctx, expected)
}

// DeleteUnrecoverableAwaitingRecovery removes a provider session proven
// absent after startup resume attempts, so no permanent phantom is exposed.
func (store *SessionStore) DeleteUnrecoverableAwaitingRecovery(ctx context.Context, expected domain.Session) (bool, error) {
	if expected.Status() != domain.SessionAwaitingRecovery {
		return false, fmt.Errorf("unrecoverable deletion requires awaiting recovery")
	}
	// A failed resume may still represent durable user work.  Only remove the
	// provider binding when the card is explicitly empty; pending prompts must
	// remain recoverable and visible for a later retry/close flow.
	return store.deleteProven(ctx, expected, true)
}

func (store *SessionStore) deleteProvenEmpty(ctx context.Context, expected domain.Session) (bool, error) {
	return store.deleteProven(ctx, expected, true)
}

func (store *SessionStore) deleteProven(ctx context.Context, expected domain.Session, requireEmpty bool) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := store.reload(); err != nil {
		return false, err
	}
	current, ok := store.byIntent[expected.IntentID()]
	if !ok {
		if deleted, exists := store.deletedEmpty[expected.ID()]; exists && deleted.Equal(expected) {
			return true, nil
		}
	}
	if !ok || !current.Equal(expected) {
		return false, ErrCompareAndSwapConflict
	}
	if store.telegramUI == nil {
		return false, nil
	}
	card, ok := store.telegramUI.Cards[expected.ID()]
	if requireEmpty && (!ok || !card.EmptyCloseEligible || len(card.History) != 0) {
		return false, nil
	}
	sessions := cloneSessions(store.byIntent)
	delete(sessions, expected.IntentID())
	ui := store.telegramUI.Clone()
	delete(ui.Cards, expected.ID())
	for node, history := range ui.RecentSessions {
		kept := make([]domain.SessionID, 0, len(history))
		for _, id := range history {
			if id != expected.ID() {
				kept = append(kept, id)
			}
		}
		ui.RecentSessions[node] = kept
	}
	for node, active := range ui.ActiveSessions {
		if active != expected.ID() {
			continue
		}
		delete(ui.ActiveSessions, node)
		for _, id := range ui.RecentSessions[node] {
			intent, exists := store.byID[id]
			s := sessions[intent]
			if exists && s.Status() != domain.SessionArchived {
				ui.ActiveSessions[node] = id
				break
			}
		}
	}
	if ui.SelectedNode != "" {
		ui.ActiveSession = ui.ActiveSessions[ui.SelectedNode]
	} else if ui.ActiveSession == expected.ID() {
		ui.ActiveSession = ""
	}
	if err := ui.Validate(); err != nil {
		return false, err
	}
	if err := writeSessionFile(store.path, sessions, store.checkpoint, &ui); err != nil {
		return false, fmt.Errorf("persist empty session deletion: %w", err)
	}
	store.byIntent = sessions
	delete(store.byID, expected.ID())
	store.telegramUI = &ui
	if store.deletedEmpty == nil {
		store.deletedEmpty = make(map[domain.SessionID]domain.Session)
	}
	store.deletedEmpty[expected.ID()] = expected
	return true, nil
}

// WasEmptySessionDeleted is an exact, process-local completion receipt for
// concurrent closers/watchers. Absence alone is never proof of successful close.
func (store *SessionStore) WasEmptySessionDeleted(ctx context.Context, id domain.SessionID, binding domain.ProviderBinding) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return false, err
	}
	if _, exists := store.byID[id]; exists {
		return false, nil
	}
	deleted, ok := store.deletedEmpty[id]
	if !ok {
		return false, nil
	}
	actual, bound := deleted.Binding()
	return bound && actual == binding, nil
}
