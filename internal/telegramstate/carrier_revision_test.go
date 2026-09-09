package telegramstate_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/telegramstate"
)

func TestCarrierRevisionSurvivesReopenAndABA(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cards.json")
	store, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy cards omit metadata; the first receipt establishes revision one.
	if err := store.Update(ctx, func(state *telegramstate.State) error {
		*state = validState()
		card, _ := state.Card("session-1")
		card.Carrier = telegramstate.Carrier{}
		card.LastPresentationOperation = "receipt:1"
		state.Cards[card.SessionID] = card
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for index, step := range []struct {
		message  int64
		revision uint64
	}{{42, 1}, {43, 2}, {42, 3}, {42, 3}} {
		anchor, history := fmt.Sprintf("view-%d", index), fmt.Sprintf("history-%d", index)
		if err := store.Update(ctx, func(state *telegramstate.State) error {
			card, _ := state.Card("session-1")
			card.Carrier.MessageID = step.message
			card.Carrier.ChatID = 7
			card.CarrierRevision = 99 // Caller snapshots cannot supply the counter.
			card.Page.Anchor = anchor
			card.History = []string{history}
			return state.SetCard(card)
		}); err != nil {
			t.Fatal(err)
		}
		store, err = telegramstate.OpenFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		state, err := store.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		card, _ := state.Card("session-1")
		if card.CarrierRevision != step.revision || card.LastPresentationOperation != "receipt:1" ||
			card.Page.Anchor != anchor || !reflect.DeepEqual(card.History, []string{history}) {
			t.Fatalf("carrier %d metadata/history lost: %+v", step.message, card)
		}
	}
}

func TestCarrierRevisionOverflowRejectsWithoutMutation(t *testing.T) {
	state := validState()
	card, _ := state.Card("session-1")
	card.CarrierRevision = ^uint64(0)
	state.Cards[card.SessionID] = card
	before := state.Clone()
	card.Carrier.MessageID++
	if err := state.SetCard(card); err == nil || !reflect.DeepEqual(state, before) {
		t.Fatal("overflow must reject the carrier change without mutation")
	}
	card, _ = state.Card("session-1")
	card.Page.Anchor = "history-only"
	if err := state.SetCard(card); err != nil || state.Cards[card.SessionID].CarrierRevision != ^uint64(0) {
		t.Fatal("unchanged carrier must remain writable at maximum revision")
	}
}

func TestPresentationOperationValidationRejectsWithoutMutation(t *testing.T) {
	for _, operation := range []string{" leading", "trailing ", "bad\x00id", "bad\nid", string([]byte{255}), strings.Repeat("x", 1031)} {
		state := validState()
		before := state.Clone()
		card, _ := state.Card("session-1")
		card.LastPresentationOperation = operation
		if err := state.SetCard(card); err == nil || !reflect.DeepEqual(state, before) {
			t.Fatalf("invalid presentation operation accepted or mutated state: length=%d", len(operation))
		}
	}
}
