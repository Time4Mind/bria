package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramstate"
)

func TestSessionStoreReloadSkipsUnchangedStateDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(context.Background(), "session-a"); err != nil {
		t.Fatal(err)
	}

	store.fullReloads = 0
	for range 3 {
		got, err := store.LoadActiveSession(context.Background())
		if err != nil || got != "session-a" {
			t.Fatalf("LoadActiveSession() = (%q, %v), want session-a", got, err)
		}
	}
	if store.fullReloads != 0 {
		t.Fatalf("unchanged state full reloads = %d, want 0", store.fullReloads)
	}
}

func TestSessionStoreReloadInvalidatesAfterExternalAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(context.Background(), "session-a"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := bytes.ReplaceAll(original, []byte("session-a"), []byte("session-b"))
	if bytes.Equal(original, replacement) {
		t.Fatal("replacement did not change the active session")
	}
	temporary := path + ".external-replacement"
	if err := os.WriteFile(temporary, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(temporary, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}

	store.fullReloads = 0
	got, err := store.LoadActiveSession(context.Background())
	if err != nil || got != "session-b" {
		t.Fatalf("LoadActiveSession() after replacement = (%q, %v), want session-b", got, err)
	}
	if store.fullReloads != 1 {
		t.Fatalf("replacement full reloads = %d, want 1", store.fullReloads)
	}
}

func TestSessionStoreFailedWriteNeverPublishesUncommittedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(context.Background(), "session-a"); err != nil {
		t.Fatal(err)
	}

	store.fullReloads = 0
	writeFailure := errors.New("injected durable write failure")
	store.writeFile = func(
		string,
		map[domain.IntentID]domain.Session,
		*coordinatorRecord,
		*telegramstate.State,
	) (storeFileGeneration, error) {
		return storeFileGeneration{}, writeFailure
	}
	err = store.SetActiveSession(context.Background(), "session-b")
	if !errors.Is(err, writeFailure) {
		t.Fatalf("SetActiveSession() error = %v, want injected write failure", err)
	}
	got, loadErr := store.LoadActiveSession(context.Background())
	if loadErr != nil || got != domain.SessionID("session-a") {
		t.Fatalf("LoadActiveSession() after failed write = (%q, %v), want committed session-a", got, loadErr)
	}
	if store.fullReloads != 0 {
		t.Fatalf("failed write path full reloads = %d, want 0", store.fullReloads)
	}
}
