package cardhistory_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/cardhistory"
	"bria/internal/telegramstate"
)

func TestRestoreFinalRecordsOneExactPendingOperation(t *testing.T) {
	card := a25AnchoredCard()
	for range 2 {
		if err := cardhistory.RestoreFinal(&card, "first", "answer"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Pending []string `json:"pending_final_operations"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wire.Pending, []string{"first:final"}) {
		t.Fatal("successful exact final did not atomically arm one pending operation")
	}
}

func TestBlocksExposeOnlyExactTypedFinalOperation(t *testing.T) {
	card := telegramstate.Card{
		History:         []string{"A start", "A continuation", "B final", "legacy final", "commentary"},
		HistoryKinds:    []string{"final", "final", "final", "final", "commentary"},
		HistoryTurnKeys: []string{"a", "a", "b", "", "c"},
	}
	blocks := cardhistory.Blocks(card, true)
	data, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct{ FinalOperationID string }
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"a:final", "a:final", "b:final", "", ""}
	for i := range want {
		if got[i].FinalOperationID != want[i] {
			t.Fatalf("block %d lost exact final identity: got=%q want=%q", i, got[i].FinalOperationID, want[i])
		}
	}
	if blocks[0].FinalContinuation || !blocks[1].FinalContinuation || blocks[2].FinalContinuation {
		t.Fatal("operation propagation changed final continuation identity")
	}
}

func TestRestoreFinalDoesNotRearmDeliveredExactFinalOrClearAnother(t *testing.T) {
	card := a25AnchoredCard()
	for _, id := range []string{"first", "queued"} {
		if err := cardhistory.RestoreFinal(&card, id, "same answer"); err != nil {
			t.Fatal(err)
		}
	}
	card.PendingFinalOperations = card.PendingFinalsAfter("first:final")
	for range 2 {
		if err := cardhistory.RestoreFinal(&card, "first", "same answer"); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(card.PendingFinalOperations, []string{"queued:final"}) {
		t.Fatal("duplicate restore rearmed A or erased pending B")
	}
	card.PendingFinalOperations = nil
	if err := cardhistory.RestoreFinal(&card, "queued", "same answer"); err != nil || len(card.PendingFinalOperations) != 0 {
		t.Fatal("legacy/delivered exact final was rearmed")
	}
}

func TestRestoreFinalPendingCapacityAndInvalidOperationLeaveHistoryUnchanged(t *testing.T) {
	for _, message := range []string{" first", "first\n", string([]byte{0xff}), strings.Repeat("x", 1025)} {
		card := a25AnchoredCard()
		if err := cardhistory.RestoreFinal(&card, message, "answer"); !errors.Is(err, cardhistory.ErrFinalInvalid) || !reflect.DeepEqual(card, a25AnchoredCard()) {
			t.Fatal("invalid final operation mutated card or returned wrong class")
		}
	}
	card := a25AnchoredCard()
	for i := range 512 {
		card.PendingFinalOperations = append(card.PendingFinalOperations, fmt.Sprintf("other-%d:final", i))
	}
	before, _ := json.Marshal(card)
	if err := cardhistory.RestoreFinal(&card, "first", "answer"); !errors.Is(err, cardhistory.ErrFinalCapacity) {
		t.Fatalf("pending capacity error = %v", err)
	}
	after, _ := json.Marshal(card)
	if string(before) != string(after) {
		t.Fatal("pending capacity failure changed history/custody")
	}
	card.PendingFinalOperations[0] = "first:final"
	if err := cardhistory.RestoreFinal(&card, "first", "answer"); err != nil || len(card.PendingFinalOperations) != 512 {
		t.Fatal("already-pending exact operation duplicated or incorrectly rejected")
	}
}

func TestRestoreFinalAndPendingCommitTogetherAcrossFileReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ui.json")
	store, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	card := a25AnchoredCard()
	card.SessionID, card.Page = "session-1", telegramstate.Page{Current: 1, Total: 1}
	if err := store.Update(ctx, func(s *telegramstate.State) error { return s.SetCard(card) }); err != nil {
		t.Fatal(err)
	}
	final := strings.Repeat("界", 6000)
	restore := func(final string) error {
		return store.Update(ctx, func(s *telegramstate.State) error {
			c, _ := s.Card("session-1")
			if err := cardhistory.RestoreFinal(&c, "first", final); err != nil {
				return err
			}
			return s.SetCard(c)
		})
	}
	if err := restore(final); err != nil {
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
	got, _ := state.Card("session-1")
	if !reflect.DeepEqual(got.PendingFinalOperations, []string{"first:final"}) || got.History[2]+got.History[3] != final {
		t.Fatal("split final and pending custody did not survive together")
	}
	blocks := cardhistory.Blocks(got, true)
	if blocks[2].FinalContinuation || !blocks[3].FinalContinuation {
		t.Fatal("pending final changed typed continuation identity")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore("conflict"); !errors.Is(err, cardhistory.ErrFinalConflict) {
		t.Fatal("conflicting final accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("failed restore changed physical history/pending custody")
	}
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		c, _ := s.Card("session-1")
		c.PendingFinalOperations = c.PendingFinalsAfter("first:final")
		return s.SetCard(c)
	}); err != nil {
		t.Fatal(err)
	}
	store, err = telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(final); err != nil {
		t.Fatal(err)
	}
	state, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = state.Card("session-1")
	if len(got.PendingFinalOperations) != 0 {
		t.Fatal("reopened duplicate final rearmed confirmed custody")
	}
}
