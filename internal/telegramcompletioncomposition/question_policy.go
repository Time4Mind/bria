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
	if err != nil {
		return state, false, err
	}
	if state.ActiveSession == sessionID {
		if visibility, ok := deliverer.Controller.(interface{ NativeScreenVisible(domain.SessionID) bool }); ok {
			return state, visibility.NativeScreenVisible(sessionID), nil
		}
		return state, true, nil
	}
	if deliverer.Preferences == nil {
		return state, false, nil
	}
	preferences, err := deliverer.Preferences.Snapshot(ctx)
	return state, preferences.NotifyBackgroundQuestions, err
}
