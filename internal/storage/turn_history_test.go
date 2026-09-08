package storage_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/storage"
)

func TestTurnHistoryKeepsEventsBesideTheirPromptAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "turn-history", "turn-history-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"first", "queued"} {
		if err := store.SetCardPrompt(ctx, session.ID(), prompt, prompt); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct{ text, kind string }{{"tool result", "tool"}, {"answer", "final"}} {
		if err := store.InsertCardTypedHistoryAfterPrompt(ctx, session.ID(), "first", event.text, event.kind); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.InsertCardTypedHistoryAfterPrompt(ctx, session.ID(), "missing", "rejected", "tool"); err == nil {
		t.Fatal("missing anchor accepted")
	}
	if err := store.AppendCardHistory(ctx, session.ID(), "tail"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for showTechnical, want := range map[bool][]string{
		true:  {"first", "tool result", "answer", "queued", "tail"},
		false: {"first", "answer", "queued", "tail"},
	} {
		got, err := reopened.LoadCardDisplayHistory(ctx, session.ID(), showTechnical)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("technical=%t: got %q, err=%v; want %q", showTechnical, got, err, want)
		}
	}
	prompts, err := reopened.LoadCardUserPrompts(ctx, session.ID(), 10)
	if err != nil || !reflect.DeepEqual(prompts, []string{"first", "queued"}) {
		t.Fatalf("prompts = %q, err=%v", prompts, err)
	}
}
