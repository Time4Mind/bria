package storage

import (
	"context"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
)

// AppendCardTypedHistory extends the existing history format without changing
// prompt keys, archived prompts, or legacy history readers.
func (store *SessionStore) AppendCardTypedHistory(ctx context.Context, id domain.SessionID, text, kind string) error {
	return store.appendCardHistory(ctx, id, text, kind)
}

func (store *SessionStore) LoadCardTranscript(ctx context.Context, id domain.SessionID, showTechnical bool) ([]cardtranscript.Block, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return nil, err
	}
	card, ok := state.Card(id)
	if !ok {
		return nil, nil
	}
	blocks := make([]cardtranscript.Block, 0, len(card.History))
	for index, text := range card.History {
		kind := ""
		if len(card.HistoryKinds) > index {
			kind = card.HistoryKinds[index]
		}
		if len(card.HistoryKeys) > index && card.HistoryKeys[index] != "" {
			kind = "prompt"
		}
		if kind == "tool" && !showTechnical {
			continue
		}
		blocks = append(blocks, cardtranscript.Block{Kind: kind, Text: text})
	}
	return blocks, nil
}
