package storage_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/storage"
)

func TestCardPageReadingIntentSurvivesStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	id := domain.SessionID("11111111-1111-4111-9111-111111111111")
	if err := store.AppendCardHistory(ctx, id, "kept history"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardPage(ctx, id, 2, 4, "history:2", false); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, ok := any(reopened).(interface {
		LoadCardPage(context.Context, domain.SessionID) (int, int, string, bool, bool, error)
	})
	if !ok {
		t.Fatal("reopened store cannot expose saved reading intent through a neutral boundary")
	}
	page, total, anchor, follow, found, err := reader.LoadCardPage(ctx, id)
	if err != nil || !found || page != 2 || total != 4 || anchor != "history:2" || follow {
		t.Fatalf("page=%d total=%d anchor=%q follow=%t found=%t err=%v", page, total, anchor, follow, found, err)
	}
	_, _, _, _, found, err = reader.LoadCardPage(ctx, "missing")
	if err != nil || found {
		t.Fatalf("missing card must remain absent: found=%t err=%v", found, err)
	}
}
