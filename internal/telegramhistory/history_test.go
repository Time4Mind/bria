package telegramhistory_test

import (
	"reflect"
	"testing"

	"bria/internal/telegramhistory"
	"bria/internal/telegramstate"
)

func TestAppendTrimsTurnMetadataTogetherWithHistory(t *testing.T) {
	card := telegramstate.Card{
		History: make([]string, 512), HistoryKeys: make([]string, 512),
		HistoryKinds: make([]string, 512), HistoryTurnKeys: make([]string, 512),
	}
	card.History[0], card.HistoryKeys[0] = "oldest", "expired"
	card.History[1], card.HistoryKinds[1], card.HistoryTurnKeys[1] = "kept tool", "tool", "first"
	card.History[511], card.HistoryKeys[511] = "queued", "second"
	telegramhistory.Append(&card, "tail", "final")
	if len(card.History) != 512 || len(card.HistoryKeys) != 512 || len(card.HistoryKinds) != 512 || len(card.HistoryTurnKeys) != 512 {
		t.Fatal("history metadata lost alignment")
	}
	if card.History[0] != "kept tool" || card.HistoryKinds[0] != "tool" || card.HistoryTurnKeys[0] != "first" || card.HistoryKeys[0] != "" {
		t.Fatal("oldest retained event lost its identity")
	}
	if card.History[510] != "queued" || card.HistoryKeys[510] != "second" || card.History[511] != "tail" || card.HistoryKinds[511] != "final" || card.HistoryKeys[511] != "" || card.HistoryTurnKeys[511] != "" {
		t.Fatal("tail inherited another event's metadata")
	}
}

func TestMissingPromptLeavesHistoryUnchanged(t *testing.T) {
	card := telegramstate.Card{History: []string{"legacy output"}}
	want := telegramstate.Card{History: []string{"legacy output"}}
	if err := telegramhistory.InsertAfterPrompt(&card, "unknown", "event", "tool"); err == nil {
		t.Fatal("missing anchor accepted")
	}
	if !reflect.DeepEqual(card, want) {
		t.Fatalf("rejected event changed card: %#v", card)
	}
}
