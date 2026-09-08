package storage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramstate"
)

func TestRecoveredFinalIsAnchoredAndIdempotentAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "recovered-final", "recovered-final-intent")
	if _, _, err = store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "queued"} {
		if err = store.SetCardPrompt(ctx, session.ID(), id, id); err != nil {
			t.Fatal(err)
		}
	}
	restore := func(s *storage.SessionStore, final string) error {
		if r, ok := any(s).(interface {
			RestoreAcceptedFinal(context.Context, domain.SessionID, string, string) error
		}); ok {
			return r.RestoreAcceptedFinal(ctx, session.ID(), "first", final)
		}
		return s.InsertCardTypedHistoryAfterPrompt(ctx, session.ID(), "first", final, "final")
	}
	if err = restore(store, "complete answer"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = restore(reopened, "complete answer"); err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, session.ID(), true)
	if err != nil || len(blocks) != 3 || blocks[1].Kind != "final" || blocks[1].Text != "complete answer" || blocks[2].Text != "queued" {
		t.Fatalf("restored blocks=%+v err=%v", blocks, err)
	}
	if err = restore(reopened, "conflicting answer"); err == nil {
		t.Fatal("conflicting final overwritten")
	}
}

func a25RecoveryStore(t *testing.T) (*storage.SessionStore, string, domain.SessionID) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "a25-recovered-final", "a25-recovered-final-intent")
	if _, _, err := store.PutStartingIfAbsent(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first", "queued"} {
		if err := store.SetCardPrompt(context.Background(), session.ID(), message, message); err != nil {
			t.Fatal(err)
		}
	}
	return store, path, session.ID()
}

func TestA25RecoveredUnicodeFinalConcurrentDuplicatesAndReopen(t *testing.T) {
	ctx := context.Background()
	store, path, id := a25RecoveryStore(t)
	// More than 16 KiB; both a multi-byte boundary and queued prompt survive.
	final := "начало:" + strings.Repeat("界🙂e\u0301", 2600) + "конец"
	if len(final) <= 16<<10 || len(final) > 32<<10 {
		t.Fatal("fixture must straddle one persisted history entry")
	}
	stores := []*storage.SessionStore{store}
	for i := 0; i < 3; i++ {
		reopened, err := storage.OpenSessionStore(path)
		if err != nil {
			t.Fatal(err)
		}
		stores = append(stores, reopened)
	}
	start := make(chan struct{})
	errors := make(chan error, 16)
	var wait sync.WaitGroup
	for i := 0; i < cap(errors); i++ {
		wait.Add(1)
		go func(s *storage.SessionStore) {
			defer wait.Done()
			<-start
			errors <- s.RestoreAcceptedFinal(ctx, id, "first", final)
		}(stores[i%len(stores)])
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.RestoreAcceptedFinal(ctx, id, "first", final); err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, id, true)
	if err != nil || len(blocks) != 4 || blocks[0].Text != "first" || blocks[3].Text != "queued" || blocks[1].Kind != "final" || blocks[2].Kind != "final" || blocks[1].Text+blocks[2].Text != final {
		t.Fatalf("reopened final lost exact text/order: blocks=%d err=%v", len(blocks), err)
	}
	for _, block := range blocks {
		if !utf8.ValidString(block.Text) || len(block.Text) > 16<<10 {
			t.Fatal("invalid UTF-8 or oversized persisted history entry")
		}
	}
}

func TestA25RecoveredFinalFailuresLeavePhysicalStateAtomic(t *testing.T) {
	for _, failure := range []string{"conflict", "missing-anchor", "capacity"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			store, path, id := a25RecoveryStore(t)
			message, final := "first", "different answer"
			switch failure {
			case "conflict":
				if err := store.RestoreAcceptedFinal(ctx, id, message, "retained answer"); err != nil {
					t.Fatal(err)
				}
			case "missing-anchor":
				message = "missing"
			case "capacity":
				final = strings.Repeat("界", 6000)
				if err := store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
					card := state.Cards[id]
					card.History = make([]string, 511)
					card.HistoryKeys = make([]string, 511)
					card.HistoryKinds = make([]string, 511)
					card.HistoryTurnKeys = make([]string, 511)
					for i := range card.History {
						card.History[i] = "retained history"
					}
					card.History[0], card.HistoryKeys[0], card.HistoryKinds[0] = "first", "first", "prompt"
					card.History[510], card.HistoryKeys[510], card.HistoryKinds[510] = "queued", "queued", "prompt"
					return state.SetCard(card)
				}); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			stateBefore, err := store.LoadTelegramUI(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RestoreAcceptedFinal(ctx, id, message, final); err == nil {
				t.Fatal("unsafe recovery unexpectedly succeeded")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected restoration changed physical state: err=%v", err)
			}
			reopened, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			stateAfter, err := reopened.LoadTelegramUI(ctx)
			if err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
				t.Fatalf("rejected restoration changed reopened UI state: err=%v", err)
			}
			if failure == "capacity" {
				// The failed two-part write must not consume the last available slot.
				if err := reopened.RestoreAcceptedFinal(ctx, id, "first", "small proven final"); err != nil {
					t.Fatal(err)
				}
				blocks, err := reopened.LoadCardTranscript(ctx, id, true)
				if err != nil || len(blocks) != 512 || blocks[1].Text != "small proven final" || blocks[511].Text != "queued" {
					t.Fatalf("capacity rollback did not preserve retry: blocks=%d err=%v", len(blocks), err)
				}
			}
		})
	}
}

func TestA25PromptWriteRereadsRecoveryAndOnlyUpdatesExistingMessage(t *testing.T) {
	ctx := context.Background()
	consumer, path, id := a25RecoveryStore(t)
	starting, err := consumer.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "a25-provider", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.Replace(ctx, starting, ready); err != nil {
		t.Fatal(err)
	}
	observed, err := consumer.Load(ctx, id)
	if err != nil || observed.Status() != domain.SessionReady {
		t.Fatalf("consumer did not observe ready before recovery: %v", err)
	}

	// Deterministic interleaving: a separate store persists recovery after the
	// consumer's ready read but before its prompt write. No sleeps or polling.
	recovery, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	awaiting, err := observed.AwaitRecovery()
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.Replace(ctx, observed, awaiting); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Equal text is not equal identity: only the existing message key may update.
	if err := consumer.SetCardPrompt(ctx, id, "new-message", "first"); err == nil {
		t.Fatal("stale ready observation admitted a new message after recovery")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected prompt changed physical state: %v", err)
	}
	if err := consumer.SetCardPrompt(ctx, id, "first", "existing request: outcome unknown"); err != nil {
		t.Fatalf("recovery blocked updating an already accepted prompt: %v", err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.Load(ctx, id)
	if err != nil || !current.Equal(awaiting) {
		t.Fatalf("prompt update changed recovery lifecycle: %v", err)
	}
	state, err := reopened.LoadTelegramUI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	card := state.Cards[id]
	if !reflect.DeepEqual(card.History, []string{"existing request: outcome unknown", "queued"}) || !reflect.DeepEqual(card.HistoryKeys, []string{"first", "queued"}) {
		t.Fatal("reopened prompt history duplicated, reordered, or lost its existing anchors")
	}
}
