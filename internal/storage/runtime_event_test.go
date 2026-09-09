package storage_test

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"bria/internal/storage"
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
