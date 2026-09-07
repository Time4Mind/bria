package telegramstate_test

import (
	"testing"
)

func TestStateClonesAndValidatesPositionallyAlignedHistoryKinds(t *testing.T) {
	state := validState()
	card := state.Cards[state.ActiveSession]
	card.History = []string{"ordinary", "technical"}
	card.HistoryKinds = []string{"", "tool"}
	state.Cards[state.ActiveSession] = card
	if err := state.Validate(); err != nil {
		t.Fatalf("typed history rejected: %v", err)
	}
	clone := state.Clone()
	clonedCard := clone.Cards[state.ActiveSession]
	clonedCard.HistoryKinds[1] = ""
	clone.Cards[state.ActiveSession] = clonedCard
	if got := state.Cards[state.ActiveSession].HistoryKinds[1]; got != "tool" {
		t.Fatalf("clone mutation changed original kind to %q", got)
	}

	legacy := validState()
	legacyCard := legacy.Cards[legacy.ActiveSession]
	legacyCard.History = []string{"untyped legacy"}
	legacy.Cards[legacy.ActiveSession] = legacyCard
	if err := legacy.Validate(); err != nil {
		t.Fatalf("missing legacy history kinds rejected: %v", err)
	}

	misaligned := state.Clone()
	misalignedCard := misaligned.Cards[misaligned.ActiveSession]
	misalignedCard.HistoryKinds = []string{"tool"}
	misaligned.Cards[misaligned.ActiveSession] = misalignedCard
	if err := misaligned.Validate(); err == nil {
		t.Fatal("misaligned history kinds accepted")
	}

	unknown := state.Clone()
	unknownCard := unknown.Cards[unknown.ActiveSession]
	unknownCard.HistoryKinds[1] = "inferred-from-text"
	unknown.Cards[unknown.ActiveSession] = unknownCard
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown history kind accepted")
	}
}
