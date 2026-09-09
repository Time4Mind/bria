package storage

import (
	"context"

	"bria/internal/domain"
)

// LoadCardPage exposes saved reading intent without leaking transport state to
// the controller. Missing cards remain missing; this read never changes history.
func (store *SessionStore) LoadCardPage(ctx context.Context, id domain.SessionID) (current, total int, anchor string, follow, found bool, err error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return 0, 0, "", false, false, err
	}
	card, found := state.Card(id)
	return card.Page.Current, card.Page.Total, card.Page.Anchor, card.Page.FollowLatest, found, nil
}
