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
	snapshot, err := store.LoadCardTranscriptSnapshot(ctx, id, showTechnical)
	return snapshot.Blocks, err
}

func (store *SessionStore) LoadCardTranscriptSnapshot(ctx context.Context, id domain.SessionID, showTechnical bool) (cardtranscript.Snapshot, error) {
	state, err := store.LoadTelegramUI(ctx)
	card, ok := state.Card(id)
	if err != nil || !ok {
		return cardtranscript.Snapshot{}, err
	}
	return cardtranscript.Snapshot{
		Blocks: cardhistory.Blocks(card, showTechnical), LastEventUnixNano: card.LastEventUnixNano,
	}, nil
}

// LoadCardProjectionSnapshot returns every durable input needed for one card
// projection after a single state-file reload.
func (store *SessionStore) LoadCardProjectionSnapshot(ctx context.Context, id domain.SessionID) (cardtranscript.Snapshot, int, int, string, bool, bool, []domain.Session, map[domain.SessionID]bool, error) {
	if err := ctx.Err(); err != nil {
		return cardtranscript.Snapshot{}, 0, 0, "", false, false, nil, nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return cardtranscript.Snapshot{}, 0, 0, "", false, false, nil, nil, err
	}
	sessions := store.listLoadedSessions()
	empty := make(map[domain.SessionID]bool)
	if store.telegramUI == nil {
		return cardtranscript.Snapshot{}, 0, 0, "", false, false, sessions, empty, nil
	}
	for sessionID, candidate := range store.telegramUI.Cards {
		if candidate.EmptyCloseEligible && len(candidate.History) == 0 {
			empty[sessionID] = true
		}
	}
	card, found := store.telegramUI.Card(id)
	if !found {
		return cardtranscript.Snapshot{}, 0, 0, "", false, false, sessions, empty, nil
	}
	transcript := cardtranscript.Snapshot{Blocks: cardhistory.Blocks(card, true), LastEventUnixNano: card.LastEventUnixNano}
	return transcript, card.Page.Current, card.Page.Total, card.Page.Anchor, card.Page.FollowLatest, true, sessions, empty, nil
}
