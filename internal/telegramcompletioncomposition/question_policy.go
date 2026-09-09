package telegramcompletioncomposition

import (
	"context"
	"errors"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

func (deliverer CompletionDeliverer) questionPolicy(ctx context.Context, sessionID domain.SessionID) (telegramstate.State, bool, error) {
	if deliverer.Cards == nil {
		return telegramstate.State{}, false, errors.New("question card store is required")
	}
	state, err := deliverer.Cards.Load(ctx)
	if err != nil || state.ActiveSession == sessionID || deliverer.Preferences == nil {
		return state, err == nil && state.ActiveSession == sessionID, err
	}
	preferences, err := deliverer.Preferences.Snapshot(ctx)
	return state, preferences.NotifyBackgroundQuestions, err
}
