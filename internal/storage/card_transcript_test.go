package storage_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/cardtranscript"
	"bria/internal/storage"
)

func TestTypedTranscriptSurvivesRestartAndPromptReplacement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "typed-transcript", "typed-transcript-intent")
	if _, _, err = store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err = store.SetCardPrompt(ctx, session.ID(), "1", "🙋‍♂ inspect"); err != nil {
		t.Fatal(err)
	}
	for _, block := range []cardtranscript.Block{{Kind: "thinking", Text: "consider"}, {Kind: "tool", Text: `{"id":"a","name":"exec","arguments":"ls"}`}, {Kind: "final", Text: "done"}} {
		if err = store.AppendCardTypedHistory(ctx, session.ID(), block.Text, block.Kind); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.SetCardPrompt(ctx, session.ID(), "1", "👨‍💻 inspect"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := reopened.LoadCardTranscript(ctx, session.ID(), false)
	want := []cardtranscript.Block{{Kind: "prompt", Text: "👨‍💻 inspect"}, {Kind: "thinking", Text: "consider"}, {Kind: "final", Text: "done"}}
	if err != nil || !reflect.DeepEqual(blocks, want) {
		t.Fatalf("blocks=%#v err=%v", blocks, err)
	}
	all, err := reopened.LoadCardTranscript(ctx, session.ID(), true)
	if err != nil || len(all) != 4 || all[2].Kind != "tool" {
		t.Fatalf("all=%#v err=%v", all, err)
	}
}
