package telegramstate_test

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

	"bria/internal/telegramstate"
)

func pendingWireCard(t *testing.T, operations []string) telegramstate.Card {
	t.Helper()
	data, err := json.Marshal(map[string]any{"session_id": "session-1", "page": map[string]int{"current": 1, "total": 1}, "carrier": map[string]int{"chat_id": 7, "message_id": 42}, "pending_final_operations": operations})
	if err != nil {
		t.Fatal(err)
	}
	var card telegramstate.Card
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	return card
}

func pendingWireOperations(t *testing.T, card telegramstate.Card) []string {
	t.Helper()
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
	return wire.Pending
}

func TestPendingFinalOperationsSurviveFileReopenAndCardGetter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ui.json")
	store, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"message-a:final", "запрос-б:final"}
	if err := store.Update(ctx, func(s *telegramstate.State) error { return s.SetCard(pendingWireCard(t, want)) }); err != nil {
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
	card, ok := state.Clone().Card("session-1")
	if !ok || !reflect.DeepEqual(pendingWireOperations(t, card), want) {
		t.Fatal("durable pending final operations disappeared after reopen/clone/getter")
	}
}

func TestPendingFinalOperationsRejectDuplicatesAtomically(t *testing.T) {
	store := telegramstate.NewMemoryStore()
	err := store.Update(context.Background(), func(s *telegramstate.State) error {
		return s.SetCard(pendingWireCard(t, []string{"a:final", "a:final"}))
	})
	if err == nil {
		t.Fatal("duplicate pending final operations accepted")
	}
	state, err := store.Load(context.Background())
	if err != nil || len(state.Cards) != 0 {
		t.Fatal("invalid pending state was committed")
	}
}

func TestPendingFinalsAfterCopiesAndRemovesOnlyExactOperation(t *testing.T) {
	for _, tc := range []struct {
		operation string
		want      []string
	}{
		{"a:final", []string{"b:final", "a:final:other"}},
		{"", []string{"a:final", "b:final", "a:final:other"}},
		{"unknown:final", []string{"a:final", "b:final", "a:final:other"}},
	} {
		card := pendingWireCard(t, []string{"a:final", "b:final", "a:final:other"})
		got := card.PendingFinalsAfter(tc.operation)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("exact removal %q = %v", tc.operation, got)
		}
		got[0] = "mutated"
		if card.PendingFinalOperations[0] != "a:final" || card.PendingFinalOperations[1] != "b:final" {
			t.Fatal("pending removal aliases source custody")
		}
	}
}

func TestPendingFinalOperationsCloneSetGetterAndAbortedUpdateAreIndependent(t *testing.T) {
	state := telegramstate.New()
	input := pendingWireCard(t, []string{"a:final", "b:final"})
	if err := state.SetCard(input); err != nil {
		t.Fatal(err)
	}
	input.PendingFinalOperations[0] = "input-mutation"
	clone := state.Clone()
	clone.Cards["session-1"].PendingFinalOperations[0] = "clone-mutation"
	got, _ := state.Card("session-1")
	got.PendingFinalOperations[0] = "getter-mutation"
	if state.Cards["session-1"].PendingFinalOperations[0] != "a:final" {
		t.Fatal("SetCard/Clone/Card leaked mutable pending slice")
	}
	store := telegramstate.NewMemoryStore()
	ctx := context.Background()
	if err := store.Update(ctx, func(s *telegramstate.State) error { *s = state.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	aborted := errors.New("abort update")
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		s.Cards["session-1"].PendingFinalOperations[0] = "aborted"
		return aborted
	}); !errors.Is(err, aborted) {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx)
	if err != nil || loaded.Cards["session-1"].PendingFinalOperations[0] != "a:final" {
		t.Fatal("aborted update changed retained pending custody")
	}
	loaded.Cards["session-1"].PendingFinalOperations[0] = "load-mutation"
	loaded, err = store.Load(ctx)
	if err != nil || loaded.Cards["session-1"].PendingFinalOperations[0] != "a:final" {
		t.Fatal("Load returned aliased custody")
	}
}

func TestPendingFinalOperationsValidateBoundsAndOmitLegacyEmpty(t *testing.T) {
	full := make([]string, 512)
	for i := range full {
		full[i] = fmt.Sprintf("message-%d:final", i)
	}
	for _, tc := range []struct {
		name       string
		operations []string
		valid      bool
	}{
		{"legacy", nil, true}, {"empty", []string{}, true}, {"capacity", full, true},
		{"over-capacity", append(append([]string(nil), full...), "overflow:final"), false},
		{"max-bytes", []string{strings.Repeat("a", 1024) + ":final"}, true},
		{"over-bytes", []string{strings.Repeat("a", 1025) + ":final"}, false},
		{"unicode", []string{strings.Repeat("界", 341) + "a:final"}, true},
		{"unicode-over-bytes", []string{strings.Repeat("界", 342) + ":final"}, false},
		{"blank", []string{""}, false}, {"whitespace", []string{" \t"}, false},
		{"leading-space", []string{" a:final"}, false}, {"trailing-space", []string{"a:final\n"}, false},
		{"invalid-utf8", []string{string([]byte{0xff})}, false}, {"duplicate", []string{"a:final", "a:final"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := pendingWireCard(t, nil)
			card.PendingFinalOperations = tc.operations // Preserve invalid UTF-8 for validation, not JSON replacement.
			state := telegramstate.New()
			err := state.SetCard(card)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
			if !tc.valid && len(state.Cards) != 0 {
				t.Fatal("invalid SetCard mutated state")
			}
			if tc.valid && len(tc.operations) == 0 {
				data, err := json.Marshal(card)
				if err != nil || strings.Contains(string(data), "pending_final_operations") {
					t.Fatal("legacy empty pending field was not omitted")
				}
			}
		})
	}
}

func TestPendingFinalOperationsRejectedFileUpdatePreservesPhysicalState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ui.json")
	store, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(s *telegramstate.State) error { return s.SetCard(pendingWireCard(t, []string{"a:final"})) }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(s *telegramstate.State) error {
		c, _ := s.Card("session-1")
		c.PendingFinalOperations = []string{"a:final", "a:final"}
		return s.SetCard(c)
	}); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("rejected pending update rewrote physical state")
	}
}

func TestPendingFinalsAfterLastReceiptMatchesPhysicalCardReread(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ui.json")
	store, err := telegramstate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := pendingWireCard(t, []string{"a:final"})
	want.PendingFinalOperations = want.PendingFinalsAfter("a:final")
	if err := store.Update(ctx, func(s *telegramstate.State) error { return s.SetCard(want) }); err != nil {
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
	got, ok := state.Card("session-1")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatal("confirmed card differs from physical reread after last pending operation removal")
	}
	if want.PendingFinalOperations != nil {
		t.Fatal("empty pending custody must be canonical nil")
	}
}
