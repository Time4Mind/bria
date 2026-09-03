package sessioncatalog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessioncatalog"
	"bria/internal/sessiondiscovery"
)

func TestSynchronizePersistsOneUnifiedArchiveAndRestartIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovered-sessions.json")
	store, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore() error = %v", err)
	}
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	codex := record(domain.ProviderCodex, "thread-1", "macbook", "/work/codex", now)
	claude := record(domain.ProviderClaude, "00000000-0000-4000-8000-000000000021", "linux", "/work/claude", now.Add(time.Hour))

	entries, err := store.Synchronize(context.Background(),
		staticSource{records: []sessiondiscovery.Record{codex}},
		staticSource{records: []sessiondiscovery.Record{claude}},
	)
	if err != nil {
		t.Fatalf("Synchronize() error = %v", err)
	}
	if len(entries) != 2 || entries[0].Record != claude || entries[1].Record != codex {
		t.Fatalf("entries = %#v, want unified newest-first Claude/Codex archive", entries)
	}
	if entries[0].ID == "" || entries[1].ID == "" || entries[0].ID == entries[1].ID {
		t.Fatalf("stable logical ids are not distinct: %#v", entries)
	}
	archived, err := entries[0].ArchivedSession()
	if err != nil {
		t.Fatalf("ArchivedSession() error = %v", err)
	}
	binding, ok := archived.Binding()
	if !ok || archived.ID() != entries[0].ID || archived.Status() != domain.SessionArchived ||
		archived.Provider() != claude.Provider || archived.ComputerID() != claude.ComputerID ||
		archived.Workdir() != claude.Workdir || binding.Provider != claude.Provider ||
		binding.SessionID != claude.ProviderSessionID || binding.Generation != 1 {
		t.Fatalf("archived session = %#v, binding = %#v/%t", archived.Snapshot(), binding, ok)
	}
	firstBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	replayed, err := reopened.Synchronize(context.Background(),
		staticSource{records: []sessiondiscovery.Record{codex, claude}},
	)
	if err != nil {
		t.Fatalf("replayed Synchronize() error = %v", err)
	}
	secondBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed, entries) {
		t.Fatalf("replayed entries = %#v, want exact %#v", replayed, entries)
	}
	if !reflect.DeepEqual(secondBytes, firstBytes) {
		t.Fatal("idempotent restart rewrote durable archive bytes")
	}
	sessions, err := reopened.ArchivedSessions(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("ArchivedSessions() = (%#v, %v), want two navigable sessions", sessions, err)
	}
	for index, session := range sessions {
		if session.ID() != entries[index].ID || session.Status() != domain.SessionArchived {
			t.Fatalf("archived session %d = %#v, want entry %#v", index, session.Snapshot(), entries[index])
		}
	}
}

func TestSynchronizeFailureLeavesDurableArchiveBitForBitUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovered-sessions.json")
	store, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	existing := record(domain.ProviderCodex, "thread-existing", "macbook", "/work/existing", now)
	if _, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{existing}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	discoveryFailure := errors.New("provider store unreadable")
	candidate := record(domain.ProviderClaude, "00000000-0000-4000-8000-000000000022", "linux", "/work/new", now)

	if _, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{candidate}, err: discoveryFailure}); !errors.Is(err, discoveryFailure) {
		t.Fatalf("Synchronize() error = %v, want source failure", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("failed synchronization mutated durable archive")
	}
	entries, err := store.Entries(context.Background())
	if err != nil || len(entries) != 1 || entries[0].Record != existing {
		t.Fatalf("entries after failure = (%#v, %v), want original only", entries, err)
	}
}

func TestSynchronizeRejectsConflictingRediscoveryWithoutChangingArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovered-sessions.json")
	store, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	existing := record(domain.ProviderCodex, "thread-same", "macbook", "/work/original", now)
	entries, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{existing}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	conflict := existing
	conflict.Workdir = "/work/conflict"
	conflict.UpdatedAt = conflict.UpdatedAt.Add(time.Minute)

	if _, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{conflict}}); !errors.Is(err, sessiondiscovery.ErrAmbiguousRecord) {
		t.Fatalf("Synchronize(conflict) error = %v, want ambiguity", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("ambiguous rediscovery mutated durable archive")
	}
	persisted, err := store.Entries(context.Background())
	if err != nil || !reflect.DeepEqual(persisted, entries) {
		t.Fatalf("persisted after ambiguity = (%#v, %v), want %#v", persisted, err, entries)
	}
}

func TestSynchronizeRejectsConflictingHistoryReferenceWithoutCreatingArchive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovered-sessions.json")
	store, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	first := record(domain.ProviderCodex, "thread-history", "macbook", "/work", now)
	conflict := first
	conflict.HistoryRef = "codex://different-history/thread-history"

	if _, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{first, conflict}}); !errors.Is(err, sessiondiscovery.ErrAmbiguousRecord) {
		t.Fatalf("Synchronize(history conflict) error = %v, want ambiguity", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history conflict created archive file: %v", err)
	}
}

func TestStableLogicalIDDoesNotChangeWhenProviderTimestampAdvances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovered-sessions.json")
	store, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	first := record(domain.ProviderClaude, "00000000-0000-4000-8000-000000000023", "linux", "/work", now)
	initial, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{first}})
	if err != nil {
		t.Fatal(err)
	}
	updated := first
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Hour)
	next, err := store.Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{updated}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].ID != initial[0].ID || next[0].Record.UpdatedAt != updated.UpdatedAt {
		t.Fatalf("updated entry = %#v, want stable id %q and timestamp %v", next, initial[0].ID, updated.UpdatedAt)
	}
}

func TestConcurrentStoreInstancesDoNotLoseDiscoveredSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovered-sessions.json")
	firstStore, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := sessioncatalog.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	records := []sessiondiscovery.Record{
		record(domain.ProviderCodex, "thread-concurrent", "macbook", "/work/codex", now),
		record(domain.ProviderClaude, "00000000-0000-4000-8000-000000000024", "linux", "/work/claude", now),
	}
	stores := []*sessioncatalog.FileStore{firstStore, secondStore}
	start := make(chan struct{})
	errorsByWorker := make(chan error, len(stores))
	var workers sync.WaitGroup
	for index := range stores {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			_, err := stores[index].Synchronize(context.Background(), staticSource{records: []sessiondiscovery.Record{records[index]}})
			errorsByWorker <- err
		}(index)
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent Synchronize() error = %v", err)
		}
	}
	persisted, err := firstStore.Entries(context.Background())
	if err != nil || len(persisted) != 2 {
		t.Fatalf("persisted entries = (%#v, %v), want both concurrent discoveries", persisted, err)
	}
}

func record(provider domain.Provider, providerID string, computer domain.ComputerID, workdir string, updated time.Time) sessiondiscovery.Record {
	return sessiondiscovery.Record{
		Provider: provider, ProviderSessionID: providerID, ComputerID: computer,
		Workdir: workdir, HistoryRef: string(provider) + "://session/" + providerID,
		CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated,
	}
}

type staticSource struct {
	records []sessiondiscovery.Record
	err     error
}

func (source staticSource) Discover(context.Context) ([]sessiondiscovery.Record, error) {
	return append([]sessiondiscovery.Record(nil), source.records...), source.err
}
