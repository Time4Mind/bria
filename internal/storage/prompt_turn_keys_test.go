package storage_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramstate"
)

type promptHistoryRow struct {
	text, key, kind, turn string
}

func TestSetCardPromptPreservesTurnKeysAcrossNextPromptAndReopen(t *testing.T) {
	for _, size := range []int{4, 512} {
		t.Run(fmt.Sprintf("history_%d", size), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.json")
			store, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			session := mustStartingSession(t, "prompt-turn-keys", "prompt-turn-keys-intent")
			if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
				t.Fatal(err)
			}
			var want []promptHistoryRow
			if size == 512 {
				// Older entries precede a real prompt and its three typed events.
				if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
					card, ok := state.Card(session.ID())
					if !ok {
						t.Fatal("session card is missing")
					}
					for i := 0; i < 508; i++ {
						text := fmt.Sprintf("older-%03d", i)
						card.History = append(card.History, text)
						want = append(want, promptHistoryRow{text: text})
					}
					return state.SetCard(card)
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.SetCardPrompt(ctx, session.ID(), "first", "first prompt"); err != nil {
				t.Fatal(err)
			}
			want = append(want, promptHistoryRow{text: "first prompt", key: "first"})
			for _, event := range []promptHistoryRow{
				{text: "first commentary", kind: "commentary", turn: "first"},
				{text: "first tool", kind: "tool", turn: "first"},
				{text: "first final", kind: "final", turn: "first"},
			} {
				if err := store.InsertCardTypedHistoryAfterPrompt(ctx, session.ID(), "first", event.text, event.kind); err != nil {
					t.Fatal(err)
				}
				want = append(want, event)
			}
			store = reopenPromptHistory(t, path, session.ID(), want)
			if err := store.SetCardPrompt(ctx, session.ID(), "next", "next prompt"); err != nil {
				t.Fatalf("next prompt after typed turn: %v", err)
			}
			if size == 512 {
				want = want[1:] // Only older-000 leaves the bounded history.
			}
			want = append(want, promptHistoryRow{text: "next prompt", key: "next"})
			store = reopenPromptHistory(t, path, session.ID(), want)
			// Status changes replace both the anchored earlier prompt and the
			// latest prompt in place, including when history is at its cap.
			for _, replacement := range []struct {
				index     int
				key, text string
			}{
				{len(want) - 5, "first", "first prompt completed"},
				{len(want) - 1, "next", "next prompt accepted"},
			} {
				want[replacement.index].text = replacement.text
				for replay := 0; replay < 2; replay++ {
					if err := store.SetCardPrompt(ctx, session.ID(), replacement.key, replacement.text); err != nil {
						t.Fatalf("replace %s, replay %d: %v", replacement.key, replay, err)
					}
					store = reopenPromptHistory(t, path, session.ID(), want)
				}
			}
		})
	}
}

func reopenPromptHistory(t *testing.T, path string, sessionID domain.SessionID, want []promptHistoryRow) *storage.SessionStore {
	t.Helper()
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadTelegramUI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	card, ok := state.Card(sessionID)
	if !ok {
		t.Fatal("reopened session card is missing")
	}
	for name, values := range map[string][]string{
		"history": card.History, "keys": card.HistoryKeys,
		"kinds": card.HistoryKinds, "turns": card.HistoryTurnKeys,
	} {
		if len(values) != len(want) {
			t.Fatalf("%s length = %d, want %d", name, len(values), len(want))
		}
	}
	for i, expected := range want {
		got := promptHistoryRow{card.History[i], card.HistoryKeys[i], card.HistoryKinds[i], card.HistoryTurnKeys[i]}
		if got != expected {
			t.Errorf("history[%d] = %+v, want %+v", i, got, expected)
		}
	}
	var display []string
	for _, row := range want {
		if row.kind != "tool" {
			display = append(display, row.text)
		}
	}
	got, err := store.LoadCardDisplayHistory(context.Background(), sessionID, false)
	if err != nil || !reflect.DeepEqual(got, display) {
		t.Fatalf("display = %q, err=%v; want %q", got, err, display)
	}
	return store
}
