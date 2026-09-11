package storage_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/storage"
)

func TestCardTranscriptSnapshotPersistsOnlyVisibleActivity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "card-activity", "card-activity-intent")
	if _, _, err = store.PutStartingIfAbsent(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err = store.SetCardPrompt(ctx, session.ID(), "message-1", "prompt"); err != nil {
		t.Fatal(err)
	}
	first, err := store.LoadCardTranscriptSnapshot(ctx, session.ID(), true)
	if err != nil || first.LastEventUnixNano <= 0 || len(first.Blocks) != 1 {
		t.Fatalf("first snapshot = (%#v, %v)", first, err)
	}
	if err = store.SetCardPrompt(ctx, session.ID(), "message-1", "prompt"); err != nil {
		t.Fatal(err)
	}
	idempotent, err := store.LoadCardTranscriptSnapshot(ctx, session.ID(), true)
	if err != nil || idempotent.LastEventUnixNano != first.LastEventUnixNano {
		t.Fatalf("idempotent snapshot = (%#v, %v), want timestamp %d", idempotent, err, first.LastEventUnixNano)
	}
	if err = store.AppendCardTypedHistory(ctx, session.ID(), "answer", "final"); err != nil {
		t.Fatal(err)
	}
	changed, err := store.LoadCardTranscriptSnapshot(ctx, session.ID(), true)
	if err != nil || changed.LastEventUnixNano <= first.LastEventUnixNano || len(changed.Blocks) != 2 {
		t.Fatalf("changed snapshot = (%#v, %v), want newer than %d", changed, err, first.LastEventUnixNano)
	}
	mainState, err := os.ReadFile(path)
	if err != nil || bytes.Contains(mainState, []byte("last_event_unix_nano")) {
		t.Fatalf("rollback-compatible main state contains activity extension: err=%v", err)
	}
	if sidecar, sidecarErr := os.ReadFile(path + ".card-activity.json"); sidecarErr != nil || !bytes.Contains(sidecar, []byte("last_event_unix_nano")) {
		t.Fatalf("activity sidecar missing timestamp: err=%v", sidecarErr)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.LoadCardTranscriptSnapshot(ctx, session.ID(), true)
	if err != nil || persisted.LastEventUnixNano != changed.LastEventUnixNano {
		t.Fatalf("persisted snapshot = (%#v, %v), want timestamp %d", persisted, err, changed.LastEventUnixNano)
	}
}
