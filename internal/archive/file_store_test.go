package archive_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/archive"
)

func TestFileStoreDeleteIsIdempotentAndRemovesOnlyTheBundle(t *testing.T) {
	root := t.TempDir()
	store, err := archive.NewFileStore(filepath.Join(root, "archives"))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("delete-me")
	m := manifest("delete-me", "node-a", "session-a", 1, time.Unix(100, 0).UTC())
	if err := store.Commit(context.Background(), m, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), m.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(context.Background(), m.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted archive load error=%v, want not-exist", err)
	}
	if err := store.Delete(context.Background(), m.ID); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "archives", string(m.ID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle directory stat=%v, want not-exist", err)
	}
	if err := store.Delete(context.Background(), archive.ArchiveID("../outside")); err == nil {
		t.Fatal("unsafe archive id accepted")
	}
}

func TestFileStoreDeleteRejectsDotArchiveIDsWithoutTouchingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "archives")
	store, err := archive.NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "keep")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []archive.ArchiveID{".", ".."} {
		if err := store.Delete(context.Background(), id); err == nil {
			t.Fatalf("Delete(%q) succeeded", id)
		}
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("archive root marker changed: content=%q err=%v", content, err)
	}
}
