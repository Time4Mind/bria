package storage_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/storage"
)

func TestArchiveUserPromptsRetainAuthorshipAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "archive-prompts", "archive-prompts-intent")
	if _, _, err := store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardHistory(ctx, session.ID(), "👨‍💻 assistant quoting a prompt"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, session.ID(), "one", "🙋‍♂ original speech"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, session.ID(), "one", "👨‍💻 processed speech"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardHistory(ctx, session.ID(), "tool result"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPrompt(ctx, session.ID(), "two", "🙅‍♂ second request"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for limit, want := range map[int][]string{0: nil, 1: {"👨‍💻 processed speech"}, 3: {"👨‍💻 processed speech", "🙅‍♂ second request"}} {
		got, err := reopened.LoadCardUserPrompts(ctx, session.ID(), limit)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("limit %d: %q, %v; want %q", limit, got, err, want)
		}
	}
}
