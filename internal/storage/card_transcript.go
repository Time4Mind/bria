package storage

import (
	"context"

	"bria/internal/cardeventhistory"
	"bria/internal/cardhistory"
	"bria/internal/cardtranscript"
	"bria/internal/domain"
)

// AppendCardTypedHistory extends the existing history format without changing
// prompt keys, archived prompts, or legacy history readers.
func (store *SessionStore) AppendCardTypedHistory(ctx context.Context, id domain.SessionID, text, kind string) error {
	return cardeventhistory.Append(ctx, store, id, text, kind)
}

func (store *SessionStore) LoadCardTranscript(ctx context.Context, id domain.SessionID, showTechnical bool) ([]cardtranscript.Block, error) {
	state, err := store.LoadTelegramUI(ctx)
	card, ok := state.Card(id)
	if err != nil || !ok {
		return nil, err
	}
	return cardhistory.Blocks(card, showTechnical), nil
}
