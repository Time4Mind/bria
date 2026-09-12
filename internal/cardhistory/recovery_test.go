package cardhistory_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"bria/internal/cardhistory"
	"bria/internal/telegramstate"
)

func a25AnchoredCard() telegramstate.Card {
	return telegramstate.Card{
		History:         []string{"first prompt", "tool output", "queued prompt"},
		HistoryKeys:     []string{"first", "", "queued"},
		HistoryKinds:    []string{"prompt", "tool", "prompt"},
		HistoryTurnKeys: []string{"", "first", ""},
	}
}

func TestA25RestoreFinalPreservesUnicodeTurnAndTechnicalProjection(t *testing.T) {
	card := a25AnchoredCard()
	final := strings.Repeat("界", 6000)
	if err := cardhistory.RestoreFinal(&card, "first", final); err != nil {
		t.Fatal(err)
	}
	want := telegramstate.Card{
		PendingFinalOperations: []string{"first:final"},
		History:                []string{"first prompt", "tool output", strings.Repeat("界", 5461), strings.Repeat("界", 539), "queued prompt"},
		HistoryKeys:            []string{"first", "", "", "", "queued"},
		HistoryKinds:           []string{"prompt", "tool", "final", "final", "prompt"},
		HistoryTurnKeys:        []string{"", "first", "first", "first", ""},
	}
	if !reflect.DeepEqual(card, want) {
		t.Fatal("final was truncated, split within a rune, or misplaced relative to its turn")
	}
	if err := cardhistory.RestoreFinal(&card, "first", final); err != nil || !reflect.DeepEqual(card, want) {
		t.Fatalf("duplicate restoration changed exact turn: %v", err)
	}
	shown, hidden := cardhistory.Blocks(card, true), cardhistory.Blocks(card, false)
	if len(shown) != 5 || len(hidden) != 4 || hidden[1].Kind != "final" || hidden[2].Kind != "final" || hidden[1].Text+hidden[2].Text != final || hidden[3].Text != "queued prompt" || !reflect.DeepEqual(card, want) {
		t.Fatal("technical visibility hid/changed the recovered final or mutated its source")
	}
}

func TestA25RestoreFinalRejectsInvalidOrUnanchoredEvidence(t *testing.T) {
	for _, tc := range []struct{ name, message, final string }{
		{"empty-message", "", "answer"},
		{"empty-final", "first", ""},
		{"missing-anchor", "absent", "answer"},
		{"invalid-utf8", "first", string([]byte{0xff})},
		{"over-limit", "first", strings.Repeat("a", (32<<10)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := a25AnchoredCard()
			if err := cardhistory.RestoreFinal(&card, tc.message, tc.final); err == nil {
				t.Fatal("unproven/invalid final accepted")
			}
			if !reflect.DeepEqual(card, a25AnchoredCard()) {
				t.Fatal("invalid evidence changed history")
			}
		})
	}
}

func TestA25RestoreFinalDuplicateAndConflictAreScopedToExactRequest(t *testing.T) {
	card := a25AnchoredCard()
	if err := cardhistory.RestoreFinal(&card, "first", "same text"); err != nil {
		t.Fatal(err)
	}
	if err := cardhistory.RestoreFinal(&card, "queued", "same text"); err != nil {
		t.Fatal(err)
	}
	if len(card.History) != 5 || card.History[2] != "same text" || card.History[4] != "same text" || card.HistoryTurnKeys[2] != "first" || card.HistoryTurnKeys[4] != "queued" {
		t.Fatal("equal finals from distinct requests were conflated")
	}
	for _, message := range []string{"first", "queued"} {
		if err := cardhistory.RestoreFinal(&card, message, "same text"); err != nil {
			t.Fatal(err)
		}
		if err := cardhistory.RestoreFinal(&card, message, "conflicting text"); err == nil {
			t.Fatal("conflicting final replaced proven result")
		}
	}
	if len(card.History) != 5 || card.History[2] != "same text" || card.History[4] != "same text" {
		t.Fatal("duplicate/conflict corrupted retained finals")
	}
}

func TestA25RestoreFinalCapacityAndMaximumSupportedAnswer(t *testing.T) {
	for _, entries := range []int{510, 511, 512} {
		card := telegramstate.Card{History: make([]string, entries), HistoryKeys: make([]string, entries), HistoryKinds: make([]string, entries)}
		for i := range card.History {
			card.History[i] = "retained"
		}
		card.HistoryKeys[0], card.HistoryKinds[0] = "first", "prompt"
		final := strings.Repeat("🙂", 8192) // Exactly 32 KiB, two valid history entries.
		err := cardhistory.RestoreFinal(&card, "first", final)
		if err != nil || len(card.History) != 512 || card.History[1]+card.History[2] != final || !utf8.ValidString(card.History[1]) || !utf8.ValidString(card.History[2]) {
			t.Fatalf("maximum supported final did not fit from %d entries: %v", entries, err)
		}
		if err := cardhistory.RestoreFinal(&card, "first", final); err != nil || len(card.History) != 512 {
			t.Fatalf("idempotent restore at capacity failed from %d entries: %v", entries, err)
		}
	}
	card := telegramstate.Card{
		History: make([]string, 512), HistoryKeys: make([]string, 512), HistoryKinds: make([]string, 512),
	}
	for index := range card.History {
		card.History[index], card.HistoryKeys[index], card.HistoryKinds[index] = "prompt", fmt.Sprintf("prompt-%d", index), "prompt"
	}
	card.HistoryKeys[0] = "first"
	before := card
	before.History, before.HistoryKeys, before.HistoryKinds = slices.Clone(card.History), slices.Clone(card.HistoryKeys), slices.Clone(card.HistoryKinds)
	if err := cardhistory.RestoreFinal(&card, "first", "answer"); !errors.Is(err, cardhistory.ErrFinalCapacity) {
		t.Fatalf("all-prompt capacity error = %v", err)
	}
	if !reflect.DeepEqual(card, before) {
		t.Fatal("all-prompt capacity failure partially mutated card")
	}
}
