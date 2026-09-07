package storage

import (
	"context"

	"bria/internal/domain"
)

// LoadCardUserPrompts returns only retained, explicitly keyed user entries.
// Untyped legacy history is not evidence of authorship, even with an emoji.
func (store *SessionStore) LoadCardUserPrompts(ctx context.Context, id domain.SessionID, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return nil, err
	}
	card, ok := state.Cards[id]
	if !ok || len(card.HistoryKeys) != len(card.History) {
		return nil, nil
	}
	var prompts []string
	for i, key := range card.HistoryKeys {
		if key != "" {
			prompts = append(prompts, card.History[i])
			if len(prompts) == limit {
				break
			}
		}
	}
	return prompts, nil
}
