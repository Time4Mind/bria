package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/storage"
	"bria/internal/telegramstate"
)

func TestSessionStorePersistsAndReloadsSession(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	var _ app.SessionStore = store

	starting := mustStartingSession(t, "session-1", "intent-1")
	stored, inserted, err := store.PutStartingIfAbsent(context.Background(), starting)
	if err != nil {
		t.Fatalf("PutStartingIfAbsent() error = %v", err)
	}
	if !inserted || !sameSession(stored, starting) {
		t.Fatalf("PutStartingIfAbsent() = (%#v, %v), want inserted starting session", stored.Snapshot(), inserted)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("store permissions = %o, want %o", got, want)
	}

	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	got, ok, err := reopened.GetByIntent(context.Background(), starting.IntentID())
	if err != nil {
		t.Fatalf("GetByIntent() error = %v", err)
	}
	if !ok || !sameSession(got, starting) {
		t.Fatalf("GetByIntent() = (%#v, %v), want persisted starting session", got.Snapshot(), ok)
	}
	loaded, err := reopened.Load(context.Background(), starting.ID())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !sameSession(loaded, starting) {
		t.Fatalf("Load() = %#v, want %#v", loaded.Snapshot(), starting.Snapshot())
	}
	if _, err := reopened.Load(context.Background(), "missing"); !errors.Is(err, storage.ErrSessionNotFound) {
		t.Fatalf("missing Load() error = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionStoreRoundTripsLifecycleMetadata(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	want, err := domain.NewStartingSessionAt(
		"session-lifecycle",
		"intent-lifecycle",
		"computer-1",
		domain.ProviderCodex,
		"/workspace/project",
		createdAt,
		domain.SessionLifetime12Hours,
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := store.PutStartingIfAbsent(context.Background(), want); err != nil || !inserted {
		t.Fatalf("PutStartingIfAbsent() = (inserted=%t, err=%v)", inserted, err)
	}

	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background(), want.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("reloaded lifecycle snapshot = %#v, want %#v", got.Snapshot(), want.Snapshot())
	}
}

func TestSessionStoreMigratesLegacyDocumentAndRoundTripsTelegramUI(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	legacy := `{"version":1,"sessions":[],"coordinator":{"version":1,"revision":1,"initialized":true,"next_update_id":1}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy document: %v", err)
	}
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	got, err := store.LoadTelegramUI(context.Background())
	if err != nil {
		t.Fatalf("load migrated UI: %v", err)
	}
	if got.ActiveSession != "" || len(got.Cards) != 0 {
		t.Fatalf("legacy UI = %#v, want empty", got)
	}
	id := domain.SessionID("session-ui")
	want := telegramstate.Card{SessionID: id, Carrier: telegramstate.Carrier{ChatID: 7, MessageID: 8}, Page: telegramstate.Page{Current: 1, Total: 2, FollowLatest: true}}
	if err := store.UpdateTelegramUI(context.Background(), func(state *telegramstate.State) error {
		state.ActiveSession = id
		return state.SetCard(want)
	}); err != nil {
		t.Fatalf("update UI: %v", err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	got, err = reopened.LoadTelegramUI(context.Background())
	if err != nil {
		t.Fatalf("reload UI: %v", err)
	}
	if got.ActiveSession != id || !reflect.DeepEqual(got.Cards[id], want) {
		t.Fatalf("reloaded UI = %#v, want active card", got)
	}
	var document map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["sessions"]; !ok {
		t.Fatal("migration dropped sessions field")
	}
	if _, ok := document["coordinator"]; !ok {
		t.Fatal("migration dropped coordinator field")
	}
	if _, ok := document["telegram_ui"]; !ok {
		t.Fatal("migration did not persist telegram_ui")
	}
}

func TestSessionStoreListRereadsAndReturnsStableIntentOrder(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	first, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore(first) error = %v", err)
	}
	second, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore(second) error = %v", err)
	}

	for _, session := range []domain.Session{
		mustStartingSession(t, "session-z", "intent-z"),
		mustStartingSession(t, "session-a", "intent-a"),
		mustStartingSession(t, "session-m", "intent-m"),
	} {
		if _, _, err := first.PutStartingIfAbsent(context.Background(), session); err != nil {
			t.Fatalf("PutStartingIfAbsent(%q) error = %v", session.IntentID(), err)
		}
	}

	got, err := second.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	gotIntentIDs := make([]domain.IntentID, 0, len(got))
	for _, session := range got {
		gotIntentIDs = append(gotIntentIDs, session.IntentID())
	}
	wantIntentIDs := []domain.IntentID{"intent-a", "intent-m", "intent-z"}
	if !reflect.DeepEqual(gotIntentIDs, wantIntentIDs) {
		t.Fatalf("List() intent order = %v, want %v", gotIntentIDs, wantIntentIDs)
	}

	late := mustStartingSession(t, "session-b", "intent-b")
	if _, _, err := first.PutStartingIfAbsent(context.Background(), late); err != nil {
		t.Fatalf("late PutStartingIfAbsent() error = %v", err)
	}
	got, err = second.List(context.Background())
	if err != nil {
		t.Fatalf("List() after external write error = %v", err)
	}
	gotIntentIDs = gotIntentIDs[:0]
	for _, session := range got {
		gotIntentIDs = append(gotIntentIDs, session.IntentID())
	}
	wantIntentIDs = []domain.IntentID{"intent-a", "intent-b", "intent-m", "intent-z"}
	if !reflect.DeepEqual(gotIntentIDs, wantIntentIDs) {
		t.Fatalf("List() after external write intent order = %v, want %v", gotIntentIDs, wantIntentIDs)
	}
}

func TestSessionStoreImportsArchivedBatchAtomicallyAndIdempotently(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	existing := mustStartingSession(t, "live-session", "live-intent")
	if _, _, err := store.PutStartingIfAbsent(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	first := mustImportedArchivedSession(t, "11111111-1111-8111-8111-111111111111", "discovered:first", domain.ProviderCodex, "thread-first", "/work/first")
	second := mustImportedArchivedSession(t, "22222222-2222-8222-8222-222222222222", "discovered:second", domain.ProviderClaude, "00000000-0000-4000-8000-000000000061", "/work/second")

	if err := store.ImportArchived(context.Background(), []domain.Session{first, second}); err != nil {
		t.Fatalf("ImportArchived() error = %v", err)
	}
	beforeReplay, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportArchived(context.Background(), []domain.Session{second, first}); err != nil {
		t.Fatalf("idempotent ImportArchived() error = %v", err)
	}
	afterReplay, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterReplay, beforeReplay) {
		t.Fatal("idempotent archive import rewrote durable bytes")
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []domain.Session{existing, first, second} {
		got, err := reopened.Load(context.Background(), want.ID())
		if err != nil || !got.Equal(want) {
			t.Fatalf("Load(%q) = (%#v, %v), want %#v", want.ID(), got.Snapshot(), err, want.Snapshot())
		}
	}
}

func TestSessionStoreRejectsWholeArchivedBatchOnConflictWithoutWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	existing := mustImportedArchivedSession(t, "11111111-1111-8111-8111-111111111111", "discovered:first", domain.ProviderCodex, "thread-original", "/work/original")
	if err := store.ImportArchived(context.Background(), []domain.Session{existing}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	valid := mustImportedArchivedSession(t, "22222222-2222-8222-8222-222222222222", "discovered:second", domain.ProviderClaude, "00000000-0000-4000-8000-000000000062", "/work/valid")
	conflictingSnapshot := existing.Snapshot()
	conflictingSnapshot.Workdir = "/work/conflict"
	conflicting, err := domain.RestoreSession(conflictingSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ImportArchived(context.Background(), []domain.Session{valid, conflicting}); !errors.Is(err, storage.ErrInvariantConflict) {
		t.Fatalf("ImportArchived(conflict) error = %v, want ErrInvariantConflict", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("conflicting archive batch mutated durable store")
	}
	if _, err := store.Load(context.Background(), valid.ID()); !errors.Is(err, storage.ErrSessionNotFound) {
		t.Fatalf("valid sibling from rejected batch Load() error = %v, want not found", err)
	}
}

func TestSessionStoreRejectsNonArchivedImportWithoutWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportArchived(context.Background(), []domain.Session{mustStartingSession(t, "not-archived", "not-archived-intent")}); !errors.Is(err, storage.ErrInvariantConflict) {
		t.Fatalf("ImportArchived(starting) error = %v, want ErrInvariantConflict", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected import created state file: %v", err)
	}
}

func TestSessionStoreRejectsChangedArchivedProviderBindingAndGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*domain.SessionSnapshot)
	}{
		{name: "provider", change: func(snapshot *domain.SessionSnapshot) {
			snapshot.Provider = domain.ProviderClaude
			snapshot.Binding.Provider = domain.ProviderClaude
		}},
		{name: "provider session id", change: func(snapshot *domain.SessionSnapshot) {
			snapshot.Binding.SessionID = "different-provider-session"
		}},
		{name: "generation", change: func(snapshot *domain.SessionSnapshot) {
			snapshot.Binding.Generation++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sessions.json")
			store, err := storage.OpenSessionStore(path)
			if err != nil {
				t.Fatal(err)
			}
			existing := mustImportedArchivedSession(t, "11111111-1111-8111-8111-111111111111", "discovered:first", domain.ProviderCodex, "thread-original", "/work/original")
			if err := store.ImportArchived(context.Background(), []domain.Session{existing}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := existing.Snapshot()
			test.change(&snapshot)
			conflict, err := domain.RestoreSession(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ImportArchived(context.Background(), []domain.Session{conflict}); !errors.Is(err, storage.ErrInvariantConflict) {
				t.Fatalf("ImportArchived() error = %v, want ErrInvariantConflict", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("conflicting archived identity mutated durable state")
			}
		})
	}
}

func TestSessionStoreAtomicallyReplaysIntent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")

	const writers = 24
	stores := make([]*storage.SessionStore, writers)
	for index := range stores {
		store, err := storage.OpenSessionStore(path)
		if err != nil {
			t.Fatalf("OpenSessionStore() error = %v", err)
		}
		stores[index] = store
	}
	results := make(chan putResult, writers)
	var group sync.WaitGroup
	for i := 0; i < writers; i++ {
		candidate := mustStartingSession(t, domain.SessionID("session-"+string(rune('a'+i))), "shared-intent")
		store := stores[i]
		group.Add(1)
		go func() {
			defer group.Done()
			stored, inserted, err := store.PutStartingIfAbsent(context.Background(), candidate)
			results <- putResult{session: stored, inserted: inserted, err: err}
		}()
	}
	group.Wait()
	close(results)

	insertedCount := 0
	var winner domain.Session
	storedSessions := make([]domain.Session, 0, writers)
	for result := range results {
		if result.err != nil {
			t.Fatalf("PutStartingIfAbsent() error = %v", result.err)
		}
		if result.inserted {
			insertedCount++
			winner = result.session
		}
		storedSessions = append(storedSessions, result.session)
	}
	if got, want := insertedCount, 1; got != want {
		t.Fatalf("inserted count = %d, want %d", got, want)
	}
	for _, stored := range storedSessions {
		if !sameSession(stored, winner) {
			t.Fatalf("replay returned %#v, want winner %#v", stored.Snapshot(), winner.Snapshot())
		}
	}

	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	persisted, ok, err := reopened.GetByIntent(context.Background(), "shared-intent")
	if err != nil {
		t.Fatalf("GetByIntent() error = %v", err)
	}
	if !ok || !sameSession(persisted, winner) {
		t.Fatalf("persisted winner = (%#v, %v), want %#v", persisted.Snapshot(), ok, winner.Snapshot())
	}
}

func TestSessionStoreAtomicallyReplaysIntentAcrossParentSymlinkAlias(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatalf("mkdir real parent: %v", err)
	}
	aliasParent := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatalf("symlink parent alias: %v", err)
	}
	realStore, err := storage.OpenSessionStore(filepath.Join(realParent, "sessions.json"))
	if err != nil {
		t.Fatalf("open real path: %v", err)
	}
	aliasStore, err := storage.OpenSessionStore(filepath.Join(aliasParent, "sessions.json"))
	if err != nil {
		t.Fatalf("open alias path: %v", err)
	}

	const writers = 32
	start := make(chan struct{})
	results := make(chan putResult, writers)
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		store := realStore
		if index%2 == 1 {
			store = aliasStore
		}
		candidate := mustStartingSession(
			t,
			domain.SessionID("alias-session-"+string(rune('a'+index))),
			"alias-intent",
		)
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			stored, inserted, err := store.PutStartingIfAbsent(context.Background(), candidate)
			results <- putResult{session: stored, inserted: inserted, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	insertedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("PutStartingIfAbsent() error = %v", result.err)
		}
		if result.inserted {
			insertedCount++
		}
	}
	if got, want := insertedCount, 1; got != want {
		t.Fatalf("inserted count across real and alias paths = %d, want %d", got, want)
	}
}

func TestSessionStoreCompareAndSwapIsDurableAndRejectsStaleState(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	starting := mustStartingSession(t, "session-1", "intent-1")
	if _, _, err := store.PutStartingIfAbsent(context.Background(), starting); err != nil {
		t.Fatalf("PutStartingIfAbsent() error = %v", err)
	}
	ready, err := starting.Ready(domain.ProviderBinding{
		Provider:   domain.ProviderCodex,
		SessionID:  "provider-session-1",
		Generation: 1,
	})
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if err := store.CompareAndSwap(context.Background(), starting, ready); err != nil {
		t.Fatalf("CompareAndSwap() error = %v", err)
	}

	awaiting, err := starting.AwaitRecovery()
	if err != nil {
		t.Fatalf("AwaitRecovery() error = %v", err)
	}
	if err := store.CompareAndSwap(context.Background(), starting, awaiting); !errors.Is(err, storage.ErrCompareAndSwapConflict) {
		t.Fatalf("stale CompareAndSwap() error = %v, want ErrCompareAndSwapConflict", err)
	}

	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	persisted, ok, err := reopened.GetByIntent(context.Background(), starting.IntentID())
	if err != nil {
		t.Fatalf("GetByIntent() error = %v", err)
	}
	if !ok || !sameSession(persisted, ready) {
		t.Fatalf("persisted session = (%#v, %v), want ready %#v", persisted.Snapshot(), ok, ready.Snapshot())
	}
}

func TestSessionStoreCompareAndSwapRejectsImmutableChanges(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	starting := mustStartingSession(t, "session-1", "intent-1")
	if _, _, err := store.PutStartingIfAbsent(context.Background(), starting); err != nil {
		t.Fatalf("PutStartingIfAbsent() error = %v", err)
	}
	changedSnapshot := starting.Snapshot()
	changedSnapshot.Workdir = "/different/workspace"
	changed, err := domain.RestoreSession(changedSnapshot)
	if err != nil {
		t.Fatalf("RestoreSession() error = %v", err)
	}
	if err := store.CompareAndSwap(context.Background(), starting, changed); !errors.Is(err, storage.ErrInvariantConflict) {
		t.Fatalf("CompareAndSwap() error = %v, want ErrInvariantConflict", err)
	}
	persisted, ok, err := store.GetByIntent(context.Background(), starting.IntentID())
	if err != nil {
		t.Fatalf("GetByIntent() error = %v", err)
	}
	if !ok || !sameSession(persisted, starting) {
		t.Fatalf("persisted session = (%#v, %v), want unchanged %#v", persisted.Snapshot(), ok, starting.Snapshot())
	}
}

func TestSessionStoreRejectsInvalidOrUnsupportedFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data string
	}{
		{name: "malformed", data: `{`},
		{name: "trailing data", data: `{"version":1,"sessions":[]} {}`},
		{name: "unsupported version", data: `{"version":2,"sessions":[]}`},
		{name: "duplicate top-level field", data: `{"version":1,"version":1,"sessions":[]}`},
		{name: "non-canonical field casing", data: `{"Version":1,"sessions":[]}`},
		{name: "duplicate nested coordinator field", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":1,"revision":1,"initialized":true,"next_update_id":1,"next_update_id":2}}`},
		{name: "unknown coordinator field", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":1,"revision":1,"initialized":true,"next_update_id":1,"extra":true}}`},
		{name: "unsupported coordinator version", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":2,"revision":1,"initialized":true,"next_update_id":1}}`},
		{name: "zero coordinator revision", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":1,"revision":0,"initialized":true,"next_update_id":1}}`},
		{name: "uninitialized coordinator with progress", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":1,"revision":1,"initialized":false,"next_update_id":1}}`},
		{name: "unsupported outbound phase", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":1,"revision":1,"initialized":true,"next_update_id":1,"outbound":{` +
			`"operation_id":"status:1","update_id":1,"status":{"conversation_id":1,"text":"safe"},"phase":"sent"}}}`},
		{name: "confirmed outbound without receipt", data: `{"version":1,"sessions":[],"coordinator":{` +
			`"version":1,"revision":1,"initialized":true,"next_update_id":2,"outbound":{` +
			`"operation_id":"status:1","update_id":1,"status":{"conversation_id":1,"text":"safe"},"phase":"confirmed"}}}`},
		{name: "invalid snapshot", data: `{"version":1,"sessions":[{"id":"session-1"}]}`},
		{name: "duplicate intent", data: `{"version":1,"sessions":[` +
			`{"id":"session-1","intent_id":"intent-1","computer_id":"computer-1","provider":"codex","workdir":"/workspace","status":"starting"},` +
			`{"id":"session-2","intent_id":"intent-1","computer_id":"computer-1","provider":"codex","workdir":"/workspace","status":"starting"}` +
			`]}`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "sessions.json")
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if _, err := storage.OpenSessionStore(path); err == nil {
				t.Fatal("OpenSessionStore() error = nil, want validation failure")
			}
		})
	}
}

func TestSessionStoreRepairsUnsafeFilePermissionsOnOpen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"sessions":[]}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}
	if _, err := storage.OpenSessionStore(path); err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("repaired permissions = %o, want %o", got, want)
	}
}

func TestSessionStoreRejectsStoreFileSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"sessions":[]}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	alias := filepath.Join(root, "sessions.json")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("symlink store file: %v", err)
	}
	if _, err := storage.OpenSessionStore(alias); err == nil {
		t.Fatal("OpenSessionStore() error = nil, want store-file symlink rejection")
	}
}

func TestSessionStorePreservesCoordinatorCheckpointInUnifiedStateDocument(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	fixture := `{
  "version": 1,
  "sessions": [],
  "coordinator": {
    "version": 1,
    "revision": 7,
    "initialized": true,
    "next_update_id": 42,
    "blocked": {"update_id": 42, "reason": "unknown update"},
    "outbound": {
      "operation_id": "status:42",
      "update_id": 42,
      "status": {"conversation_id": 123, "text": "safe status"},
      "phase": "unknown"
    }
  }
}`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	if _, _, err := store.PutStartingIfAbsent(
		context.Background(),
		mustStartingSession(t, "session-1", "intent-1"),
	); err != nil {
		t.Fatalf("PutStartingIfAbsent() error = %v", err)
	}

	var document struct {
		Coordinator json.RawMessage `json:"coordinator"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unified state: %v", err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode unified state: %v", err)
	}
	var got any
	var want any
	if err := json.Unmarshal(document.Coordinator, &got); err != nil {
		t.Fatalf("decode persisted coordinator: %v", err)
	}
	if err := json.Unmarshal([]byte(`{
    "version": 1,
    "revision": 7,
    "initialized": true,
    "next_update_id": 42,
    "blocked": {"update_id": 42, "reason": "unknown update"},
    "outbound": {
      "operation_id": "status:42",
      "update_id": 42,
      "status": {"conversation_id": 123, "text": "safe status"},
      "phase": "unknown"
    }
  }`), &want); err != nil {
		t.Fatalf("decode expected coordinator: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coordinator after session write = %#v, want %#v", got, want)
	}
}

func TestSessionStorePersistsActiveCardCarrier(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "carrier-session", "carrier-intent")
	if _, _, err := store.PutStartingIfAbsent(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(context.Background(), session.ID()); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCardCarrier(context.Background(), session.ID(), 449692402, 12345); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadTelegramUI(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	card, ok := state.Card(session.ID())
	if !ok || card.Carrier != (telegramstate.Carrier{ChatID: 449692402, MessageID: 12345}) {
		t.Fatalf("card carrier = %#v, found=%v", card.Carrier, ok)
	}
}

func TestSessionStorePersistsBoundedCardHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	session := mustStartingSession(t, "history-session", "history-intent")
	if _, _, err := store.PutStartingIfAbsent(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetActiveSession(context.Background(), session.ID()); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardHistory(context.Background(), session.ID(), "event one"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendCardHistory(context.Background(), session.ID(), "event two"); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reopened.LoadCardHistory(context.Background(), session.ID())
	if err != nil || !reflect.DeepEqual(history, []string{"event one", "event two"}) {
		t.Fatalf("history = %#v, err=%v", history, err)
	}
}

type putResult struct {
	session  domain.Session
	inserted bool
	err      error
}

func mustStartingSession(t *testing.T, id domain.SessionID, intentID domain.IntentID) domain.Session {
	t.Helper()
	session, err := domain.NewStartingSession(
		id,
		intentID,
		"computer-1",
		domain.ProviderCodex,
		"/workspace/project",
	)
	if err != nil {
		t.Fatalf("NewStartingSession() error = %v", err)
	}
	return session
}

func mustImportedArchivedSession(
	t *testing.T,
	id domain.SessionID,
	intentID domain.IntentID,
	provider domain.Provider,
	providerSessionID string,
	workdir string,
) domain.Session {
	t.Helper()
	createdAt := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	session, err := domain.RestoreSession(domain.SessionSnapshot{
		ID: id, IntentID: intentID, ComputerID: "computer-1",
		Provider: provider, Workdir: workdir, Status: domain.SessionArchived,
		Binding:   &domain.ProviderBinding{Provider: provider, SessionID: providerSessionID, Generation: 1},
		CreatedAt: createdAt, StateChangedAt: createdAt.Add(time.Hour),
		Lifetime: domain.SessionLifetimeNever,
	})
	if err != nil {
		t.Fatalf("RestoreSession(archived) error = %v", err)
	}
	return session
}

func sameSession(left, right domain.Session) bool {
	return reflect.DeepEqual(left.Snapshot(), right.Snapshot())
}
