package telegramhistorylimit_test

import (
	"fmt"
	"testing"

	"bria/internal/telegramhistorylimit"
	"bria/internal/telegramstate"
)

func TestBoundedInsertEvictsKeyedEventFromAnotherTurnAndKeepsPromptAnchors(t *testing.T) {
	card := telegramstate.Card{
		History: make([]string, 512), HistoryKeys: make([]string, 512),
		HistoryKinds: make([]string, 512), HistoryTurnKeys: make([]string, 512),
	}
	card.History[0], card.HistoryKeys[0], card.HistoryKinds[0] = "active prompt", "active", "prompt"
	card.History[1], card.HistoryKeys[1], card.HistoryKinds[1], card.HistoryTurnKeys[1] = "active event", "runtime-active", "commentary", "active"
	card.History[2], card.HistoryKeys[2], card.HistoryKinds[2] = "queued prompt", "queued", "prompt"
	card.History[3], card.HistoryKeys[3], card.HistoryKinds[3], card.HistoryTurnKeys[3] = "old keyed event", "runtime-old", "tool", "old"
	for index := 4; index < len(card.History); index++ {
		card.History[index], card.HistoryKeys[index], card.HistoryKinds[index] = "retained prompt", fmt.Sprintf("queued-%d", index), "prompt"
	}
	if err := telegramhistorylimit.InsertAfterPrompt(&card, "active", "new event", "question"); err != nil {
		t.Fatal(err)
	}
	if len(card.History) != 512 || len(card.HistoryKeys) != 512 || len(card.HistoryKinds) != 512 || len(card.HistoryTurnKeys) != 512 {
		t.Fatal("bounded insert lost metadata alignment")
	}
	for _, removed := range card.History {
		if removed == "old keyed event" {
			t.Fatal("old keyed event from another turn was not evicted")
		}
	}
	if card.History[0] != "active prompt" || card.History[1] != "active event" || card.History[2] != "new event" || card.History[3] != "queued prompt" {
		t.Fatalf("turn order or prompt anchors changed: %q", card.History[:4])
	}
	if card.HistoryKinds[2] != "question" || card.HistoryTurnKeys[2] != "active" {
		t.Fatalf("new event metadata = kind %q turn %q", card.HistoryKinds[2], card.HistoryTurnKeys[2])
	}
}

func TestBoundedInsertEvictsOldPromptBeforeActiveAndKeepsQueuedPrompt(t *testing.T) {
	card := telegramstate.Card{
		History: make([]string, 512), HistoryKeys: make([]string, 512), HistoryKinds: make([]string, 512), HistoryTurnKeys: make([]string, 512),
	}
	for index := range card.History {
		card.History[index], card.HistoryKeys[index], card.HistoryKinds[index] = "old prompt", fmt.Sprintf("old-%d", index), "prompt"
	}
	card.History[510], card.HistoryKeys[510] = "active prompt", "active"
	card.History[511], card.HistoryKeys[511] = "queued prompt", "queued"
	if err := telegramhistorylimit.InsertAfterPrompt(&card, "active", "event", "commentary"); err != nil {
		t.Fatal(err)
	}
	if len(card.History) != 512 || card.History[509] != "active prompt" || card.History[510] != "event" || card.History[511] != "queued prompt" {
		t.Fatalf("prompt-only compaction lost active flow: tail=%q", card.History[509:])
	}
}

func TestBoundedInsertNeverEvictsPendingFinal(t *testing.T) {
	card := telegramstate.Card{
		History: make([]string, 512), HistoryKeys: make([]string, 512), HistoryKinds: make([]string, 512), HistoryTurnKeys: make([]string, 512),
		PendingFinalOperations: []string{"pending:final"},
	}
	card.History[0], card.HistoryKinds[0], card.HistoryTurnKeys[0] = "pending final", "final", "pending"
	card.History[1], card.HistoryKinds[1], card.HistoryTurnKeys[1] = "delivered final", "final", "delivered"
	card.History[2], card.HistoryKeys[2], card.HistoryKinds[2] = "active prompt", "active", "prompt"
	for index := 3; index < len(card.History); index++ {
		card.History[index], card.HistoryKeys[index], card.HistoryKinds[index] = "queued prompt", fmt.Sprintf("queued-%d", index), "prompt"
	}
	if err := telegramhistorylimit.InsertAfterPrompt(&card, "active", "event", "thinking"); err != nil {
		t.Fatal(err)
	}
	foundPending, foundDelivered := false, false
	for _, item := range card.History {
		foundPending = foundPending || item == "pending final"
		foundDelivered = foundDelivered || item == "delivered final"
	}
	if !foundPending || foundDelivered {
		t.Fatalf("pending final retention = %t, delivered final retention = %t", foundPending, foundDelivered)
	}
}

func TestReserveMultipleEntriesRemovesMixedPrioritiesByDescendingIndex(t *testing.T) {
	card := telegramstate.Card{
		History: make([]string, 512), HistoryKeys: make([]string, 512), HistoryKinds: make([]string, 512), HistoryTurnKeys: make([]string, 512),
	}
	card.History[0], card.HistoryKeys[0], card.HistoryKinds[0] = "old prompt", "old", "prompt"
	card.History[1], card.HistoryKinds[1], card.HistoryTurnKeys[1] = "other event", "commentary", "other"
	card.History[2], card.HistoryKeys[2], card.HistoryKinds[2] = "active prompt", "active", "prompt"
	for index := 3; index < len(card.History); index++ {
		card.History[index], card.HistoryKeys[index], card.HistoryKinds[index] = "queued prompt", fmt.Sprintf("queued-%d", index), "prompt"
	}
	if err := telegramhistorylimit.EnsureRoomAfterPrompt(&card, "active", 2); err != nil {
		t.Fatal(err)
	}
	if len(card.History) != 510 || card.History[0] != "active prompt" || card.HistoryKeys[0] != "active" {
		t.Fatalf("mixed-priority removal changed active anchor: first=%q key=%q len=%d", card.History[0], card.HistoryKeys[0], len(card.History))
	}
}
