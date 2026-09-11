// Package telegramcompletionpolicy decides whether completion-side notifications are visible.
package telegramcompletionpolicy

import (
	"context"
	"errors"

	"bria/internal/carddeliveryguard"
	"bria/internal/domain"
	"bria/internal/settingsport"
	"bria/internal/telegramstate"
)

func Question(ctx context.Context, cards carddeliveryguard.Store, preferences settingsport.Preferences, sessionID domain.SessionID) (telegramstate.State, bool, error) {
	if cards == nil {
		return telegramstate.State{}, false, errors.New("question card store is required")
	}
	state, err := cards.Load(ctx)
	if err != nil || state.ActiveSession == sessionID || preferences == nil {
		return state, err == nil && state.ActiveSession == sessionID, err
	}
	snapshot, err := preferences.Snapshot(ctx)
	return state, snapshot.NotifyBackgroundQuestions, err
}
