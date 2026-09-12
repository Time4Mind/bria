package storage_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/cardtranscript"
	"bria/internal/domain"
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

func TestCardProjectionSnapshotJoinsTranscriptPageAndSessionList(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSessionStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	selected := mustStartingSession(t, "selected", "selected-intent")
	empty := mustStartingSession(t, "empty", "standby:empty")
	for _, session := range []domain.Session{selected, empty} {
		if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AppendCardTypedHistory(ctx, selected.ID(), "done", "final"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPage(ctx, selected.ID(), 2, 3, "answer", false); err != nil {
		t.Fatal(err)
	}
	transcript, page, pages, anchor, follow, found, sessions, emptyClose, err := store.LoadCardProjectionSnapshot(ctx, selected.ID())
	if err != nil || !found || page != 2 || pages != 3 || anchor != "answer" || follow || len(sessions) != 2 {
		t.Fatalf("projection = transcript:%#v page:%d/%d anchor:%q follow:%t found:%t sessions:%d err:%v", transcript, page, pages, anchor, follow, found, len(sessions), err)
	}
	if len(transcript.Blocks) != 1 || transcript.Blocks[0].Text != "done" || !emptyClose[empty.ID()] || emptyClose[selected.ID()] {
		t.Fatalf("projection transcript/empty evidence = %#v / %#v", transcript, emptyClose)
	}
}
