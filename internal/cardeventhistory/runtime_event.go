// Package cardeventhistory commits exact native event identity through an atomic card-update port.
package cardeventhistory

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/telegramhistory"
	"bria/internal/telegramstate"
)

var ErrCardUnavailable = errors.New("runtime event card unavailable")

type Store interface {
	UpdateTelegramUI(context.Context, func(*telegramstate.State) error) error
}

func Append(ctx context.Context, store Store, id domain.SessionID, item, kind string) error {
	if id == "" || item == "" {
		return errors.New("session and history item are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[id]
		if !ok {
			card = telegramstate.Card{SessionID: id, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
		}
		card.EmptyCloseEligible = false
		telegramhistory.Append(&card, item, kind)
		card.TouchEvent(time.Now())
		return state.SetCard(card)
	})
}

func InsertTyped(ctx context.Context, store Store, id domain.SessionID, promptID, item, kind string) error {
	if id == "" || promptID == "" || item == "" {
		return fmt.Errorf("session, prompt, and history item are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[id]
		if !ok {
			return ErrCardUnavailable
		}
		if err := telegramhistory.InsertAfterPrompt(&card, promptID, item, kind); err != nil {
			return err
		}
		card.TouchEvent(time.Now())
		return state.SetCard(card)
	})
}

func Insert(ctx context.Context, store Store, id domain.SessionID, promptID, eventID, item, kind string) error {
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[id]
		if !ok {
			return ErrCardUnavailable
		}
		before := len(card.History)
		if err := InsertRuntimeEvent(&card, promptID, eventID, item, kind); err != nil {
			return err
		}
		card.EmptyCloseEligible = false
		if len(card.History) != before {
			card.TouchEvent(time.Now())
		}
		return state.SetCard(card)
	})
}

// InsertRuntimeEvent deduplicates an exact event, never merely equal text.
// Existing aligned history keys retain identity across a physical reopen.
func InsertRuntimeEvent(card *telegramstate.Card, promptID, eventID, item, kind string) error {
	if promptID == "" || eventID == "" || len(eventID) > 512 || !utf8.ValidString(eventID) || item == "" || (kind != "commentary" && kind != "tool" && kind != "question" && kind != "thinking") {
		return fmt.Errorf("invalid runtime event identity")
	}
	key := fmt.Sprintf("runtime-event:%x", sha256.Sum256([]byte(promptID+"\x00"+eventID)))
	for index, prior := range card.HistoryKeys {
		if prior != key {
			continue
		}
		if card.History[index] != item || len(card.HistoryKinds) <= index || card.HistoryKinds[index] != kind || len(card.HistoryTurnKeys) <= index || card.HistoryTurnKeys[index] != promptID {
			return fmt.Errorf("runtime event identity conflicts with retained history")
		}
		return nil
	}
	if err := telegramhistory.InsertAfterPrompt(card, promptID, item, kind); err != nil {
		return err
	}
	if len(card.HistoryKeys) == 0 {
		card.HistoryKeys = make([]string, len(card.History))
	}
	index := -1
	for i, turn := range card.HistoryTurnKeys {
		if turn == promptID {
			index = i
		}
	}
	card.HistoryKeys[index] = key
	return nil
}
