package storage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"bria/internal/storage"
	"bria/internal/telegramstate"
)

func TestRuntimeEventIdentitySurvivesReopenAndConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, mustStartingSession(t, "s", "intent")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, "s", "request", "prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, "s", "next", "queued prompt"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, err := storage.OpenSessionStore(path)
			if err == nil {
				err = other.InsertCardRuntimeEvent(ctx, "s", "request", "8123:0", "progress", "commentary")
			}
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := reopened.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := before.Cards["s"].History; !reflect.DeepEqual(got, []string{"prompt", "progress", "queued prompt"}) {
		t.Fatal(got)
	}
	if err := reopened.InsertCardRuntimeEvent(ctx, "s", "request", "8123:0", "changed", "commentary"); err == nil {
		t.Fatal("same identity replaced content")
	}
	after, err := reopened.LoadTelegramUI(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("conflict mutated history")
	}
	if err := reopened.InsertCardRuntimeEvent(ctx, "s", "request", "8124:0", "progress", "commentary"); err != nil {
		t.Fatal(err)
	}
	history, err := reopened.LoadCardHistory(ctx, "s")
	if err != nil || len(history) != 4 {
		t.Fatal("distinct identical-text event lost", err)
	}
}

func TestFullCardRetainsActivePromptAndAcceptsRuntimeEventReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutStartingIfAbsent(ctx, mustStartingSession(t, "s", "intent")); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card := telegramstate.Card{
			SessionID:       "s",
			Page:            telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
			History:         make([]string, 512),
			HistoryKeys:     make([]string, 512),
			HistoryKinds:    make([]string, 512),
			HistoryTurnKeys: make([]string, 512),
		}
		for index := 0; index < 510; index++ {
			card.History[index] = fmt.Sprintf("old-%03d", index)
			card.HistoryKinds[index] = "commentary"
		}
		card.History[510], card.HistoryKeys[510], card.HistoryKinds[510] = "active prompt", "request", "prompt"
		card.History[511], card.HistoryKeys[511], card.HistoryKinds[511] = "queued prompt", "next", "prompt"
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	events := []struct{ id, text, kind string }{
		{"event-commentary", "commentary event", "commentary"},
		{"event-tool", "tool event", "tool"},
		{"event-question", "question event", "question"},
		{"event-thinking", "thinking event", "thinking"},
	}
	for _, event := range events {
		if err := store.InsertCardRuntimeEvent(ctx, "s", "request", event.id, event.text, event.kind); err != nil {
			t.Fatalf("insert %s into full card: %v", event.kind, err)
		}
	}
	final := strings.Repeat("final ", 3000)
	if err := store.RestoreAcceptedFinal(ctx, "s", "request", final); err != nil {
		t.Fatalf("restore final after runtime events filled the card: %v", err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RestoreAcceptedFinal(ctx, "s", "request", final); err != nil {
		t.Fatalf("replay final after runtime events filled the card: %v", err)
	}
	for _, event := range events {
		if err := reopened.InsertCardRuntimeEvent(ctx, "s", "request", event.id, event.text, event.kind); err != nil {
			t.Fatalf("replay %s: %v", event.kind, err)
		}
	}
	snapshot, err := reopened.LoadTelegramUI(ctx)
	card, ok := snapshot.Card("s")
	if err != nil || !ok {
		t.Fatalf("load card: found=%t err=%v", ok, err)
	}
	if len(card.History) != 512 || len(card.HistoryKeys) != 512 || len(card.HistoryKinds) != 512 || len(card.HistoryTurnKeys) != 512 {
		t.Fatalf("unaligned bounded history: history=%d keys=%d kinds=%d turns=%d", len(card.History), len(card.HistoryKeys), len(card.HistoryKinds), len(card.HistoryTurnKeys))
	}
	if countHistory(card.History, "active prompt") != 1 || countHistory(card.History, "queued prompt") != 1 {
		t.Fatalf("prompt anchors were evicted: %#v", card.History)
	}
	for _, event := range events {
		count := 0
		for index, text := range card.History {
			if text != event.text {
				continue
			}
			count++
			if card.HistoryKinds[index] != event.kind || card.HistoryTurnKeys[index] != "request" || card.HistoryKeys[index] == "" {
				t.Fatalf("%s metadata at %d = kind %q turn %q key %q", event.kind, index, card.HistoryKinds[index], card.HistoryTurnKeys[index], card.HistoryKeys[index])
			}
		}
		if count != 1 {
			t.Fatalf("%s replay count = %d", event.kind, count)
		}
	}
	var restoredFinal strings.Builder
	for index, kind := range card.HistoryKinds {
		if kind == "final" && card.HistoryTurnKeys[index] == "request" {
			restoredFinal.WriteString(card.History[index])
		}
	}
	if restoredFinal.String() != final {
		t.Fatalf("restored final differs: got=%d bytes want=%d", restoredFinal.Len(), len(final))
	}
}

func countHistory(history []string, want string) int {
	count := 0
	for _, item := range history {
		if item == want {
			count++
		}
	}
	return count
}
