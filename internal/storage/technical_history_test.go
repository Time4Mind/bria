package storage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/storage"
	"bria/internal/telegramstate"
)

func TestSessionStorePersistsTypedTechnicalHistoryAndFiltersDisplayWithoutTextInference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "technical-history", "technical-history-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardHistory(ctx, session.ID(), "ordinary output"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardTechnicalHistory(ctx, session.ID(), "exact tool event"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, session.ID(), "telegram-update:1", "🛠 ordinary prompt text"); err != nil {
		t.Fatal(err)
	}

	for showTechnical, want := range map[bool][]string{
		false: {"ordinary output", "🛠 ordinary prompt text"},
		true:  {"ordinary output", "exact tool event", "🛠 ordinary prompt text"},
	} {
		got, err := store.LoadCardDisplayHistory(ctx, session.ID(), showTechnical)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("showTechnical=%t: history = %#v, err=%v; want %#v", showTechnical, got, err, want)
		}
	}

	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := reopened.LoadCardDisplayHistory(ctx, session.ID(), false)
	if err != nil || !reflect.DeepEqual(hidden, []string{"ordinary output", "🛠 ordinary prompt text"}) {
		t.Fatalf("reopened hidden history = %#v, err=%v", hidden, err)
	}
	full, err := reopened.LoadCardHistory(ctx, session.ID())
	if err != nil || !reflect.DeepEqual(full, []string{"ordinary output", "exact tool event", "🛠 ordinary prompt text"}) {
		t.Fatalf("unfiltered history changed = %#v, err=%v", full, err)
	}
}

func TestSessionStoreHistoryKindsStayAlignedThroughAppendTrimPromptReplacementAndReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "technical-trim", "technical-trim-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	history := make([]string, 512)
	kinds := make([]string, 512)
	keys := make([]string, 512)
	for index := range history {
		history[index] = fmt.Sprintf("event-%03d", index)
	}
	kinds[2] = "tool"
	if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Card(session.ID())
		if !ok {
			t.Fatal("session card is missing")
		}
		card.History = history
		card.HistoryKinds = kinds
		card.HistoryKeys = keys
		return state.SetCard(card)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardHistory(ctx, session.ID(), "ordinary-after-trim"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, session.ID(), "telegram-update:2", "queued prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, session.ID(), "telegram-update:2", "accepted prompt"); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card, ok := state.Card(session.ID())
	if !ok || len(card.History) != 512 || len(card.HistoryKinds) != 512 || len(card.HistoryKeys) != 512 {
		t.Fatalf("aligned card = %#v, found=%t", card, ok)
	}
	if card.History[0] != "event-002" || card.HistoryKinds[0] != "tool" || card.History[511] != "accepted prompt" || card.HistoryKinds[511] != "" || card.HistoryKeys[511] != "telegram-update:2" {
		t.Fatalf("trimmed metadata = history[0]=%q kind[0]=%q history[511]=%q kind[511]=%q key[511]=%q", card.History[0], card.HistoryKinds[0], card.History[511], card.HistoryKinds[511], card.HistoryKeys[511])
	}
	hidden, err := reopened.LoadCardDisplayHistory(ctx, session.ID(), false)
	if err != nil || len(hidden) != 511 || hidden[0] != "event-003" || hidden[510] != "accepted prompt" {
		t.Fatalf("filtered trimmed history = %#v, err=%v", hidden, err)
	}
}

func TestSessionStoreTreatsMissingHistoryKindsAsOrdinaryLegacyHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "legacy-history-kinds", "legacy-history-kinds-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"legacy ordinary", "tool-looking but untyped"} {
		if err := store.AppendCardHistory(ctx, session.ID(), item); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.LoadCardDisplayHistory(ctx, session.ID(), false)
	if err != nil || !reflect.DeepEqual(got, []string{"legacy ordinary", "tool-looking but untyped"}) {
		t.Fatalf("legacy display history = %#v, err=%v", got, err)
	}
}
