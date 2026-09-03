package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/storage"
)

func TestCoordinatorCheckpointStoreFirstSaveIsDurableAndRereadable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("OpenCoordinatorCheckpointStore() error = %v", err)
	}
	var _ coordinator.CheckpointStore = store

	if got, found, err := store.Load(context.Background()); err != nil || found {
		t.Fatalf("empty Load() = (%#v, %v, %v), want zero, false, nil", got, found, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty load created state file: %v", err)
	}

	want := fullCheckpoint(coordinator.OutboundPrepared)
	stored, err := store.Save(context.Background(), 0, want)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if stored.Revision != 1 || !reflect.DeepEqual(stored.Checkpoint, want) {
		t.Fatalf("Save() = %#v, want revision 1 and %#v", stored, want)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat state: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %v, want regular 0600", info.Mode())
	}

	reopened, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("reopen checkpoint store: %v", err)
	}
	got, found, err := reopened.Load(context.Background())
	if err != nil {
		t.Fatalf("reopened Load() error = %v", err)
	}
	if !found || !reflect.DeepEqual(got, stored) {
		t.Fatalf("reopened Load() = (%#v, %v), want %#v, true", got, found, stored)
	}
}

func TestCoordinatorAndSessionsPreserveEachOtherInOneDocument(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	sessions, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("OpenSessionStore() error = %v", err)
	}
	starting := mustStartingSession(t, "session-1", "intent-1")
	if _, _, err := sessions.PutStartingIfAbsent(context.Background(), starting); err != nil {
		t.Fatalf("PutStartingIfAbsent() error = %v", err)
	}

	checkpoints, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("OpenCoordinatorCheckpointStore() error = %v", err)
	}
	wantCheckpoint := fullCheckpoint(coordinator.OutboundConfirmed)
	wantCheckpoint.NextUpdateID = 43
	wantCheckpoint.Outbound.Receipt = &coordinator.Receipt{MessageID: 9001}
	if _, err := checkpoints.Save(context.Background(), 0, wantCheckpoint); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	ready, err := starting.Ready(providerBinding())
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if err := sessions.CompareAndSwap(context.Background(), starting, ready); err != nil {
		t.Fatalf("CompareAndSwap() error = %v", err)
	}

	reopenedSessions, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen sessions: %v", err)
	}
	gotSession, found, err := reopenedSessions.GetByIntent(context.Background(), starting.IntentID())
	if err != nil || !found || !sameSession(gotSession, ready) {
		t.Fatalf("persisted session = (%#v, %v, %v), want ready", gotSession.Snapshot(), found, err)
	}
	reopenedCheckpoints, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("reopen checkpoints: %v", err)
	}
	gotCheckpoint, found, err := reopenedCheckpoints.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(gotCheckpoint.Checkpoint, wantCheckpoint) {
		t.Fatalf("persisted checkpoint = (%#v, %v, %v), want %#v", gotCheckpoint, found, err, wantCheckpoint)
	}
}

func TestCoordinatorCheckpointStoreCASIsIdempotentAndRejectsStaleWriter(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	first, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("open first: %v", err)
	}
	second, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	initial := coordinator.Checkpoint{Initialized: true, NextUpdateID: 10}
	stored, err := first.Save(context.Background(), 0, initial)
	if err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	replay, err := second.Save(context.Background(), 0, initial)
	if err != nil || !reflect.DeepEqual(replay, stored) {
		t.Fatalf("idempotent replay = (%#v, %v), want %#v, nil", replay, err, stored)
	}
	if _, err := second.Save(context.Background(), 0, coordinator.Checkpoint{Initialized: true, NextUpdateID: 11}); !errors.Is(err, storage.ErrCompareAndSwapConflict) {
		t.Fatalf("stale Save() error = %v, want ErrCompareAndSwapConflict", err)
	}
	got, found, err := first.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(got, stored) {
		t.Fatalf("Load() after stale write = (%#v, %v, %v), want %#v, true, nil", got, found, err, stored)
	}
}

func TestCoordinatorCheckpointStorePreservesUnknownOutboundWithoutRetryMutation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("open checkpoint store: %v", err)
	}
	want := fullCheckpoint(coordinator.OutboundUnknown)
	stored, err := store.Save(context.Background(), 0, want)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reopened, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("reopen checkpoint store: %v", err)
	}
	got, found, err := reopened.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(got, stored) {
		t.Fatalf("unknown Load() = (%#v, %v, %v), want %#v", got, found, err, stored)
	}
}

func TestCoordinatorCheckpointStorePreservesCallbackQueryID(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	store, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("open checkpoint store: %v", err)
	}
	want := fullCheckpoint(coordinator.OutboundPrepared)
	want.Outbound.Status.CallbackQueryID = "callback-opaque"
	stored, err := store.Save(context.Background(), 0, want)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if !reflect.DeepEqual(stored.Checkpoint, want) {
		t.Fatalf("Save() checkpoint = %#v, want %#v", stored.Checkpoint, want)
	}
	reopened, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("reopen checkpoint store: %v", err)
	}
	got, found, err := reopened.Load(context.Background())
	if err != nil || !found || !reflect.DeepEqual(got, stored) {
		t.Fatalf("Load() = (%#v, %v, %v), want %#v, true, nil", got, found, err, stored)
	}
}

func TestCoordinatorCheckpointStoreSerializesConcurrentFirstSave(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	const writers = 24
	stores := make([]*storage.CoordinatorCheckpointStore, writers)
	for index := range stores {
		store, err := storage.OpenCoordinatorCheckpointStore(path)
		if err != nil {
			t.Fatalf("open store %d: %v", index, err)
		}
		stores[index] = store
	}
	type result struct {
		stored coordinator.StoredCheckpoint
		err    error
	}
	results := make(chan result, writers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index, store := range stores {
		index, store := index, store
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			stored, err := store.Save(context.Background(), 0, coordinator.Checkpoint{
				Initialized:  true,
				NextUpdateID: int64(100 + index),
			})
			results <- result{stored: stored, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
		case errors.Is(result.err, storage.ErrCompareAndSwapConflict):
			conflicts++
		default:
			t.Fatalf("concurrent Save() error = %v", result.err)
		}
	}
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("concurrent saves successes/conflicts = %d/%d, want 1/%d", successes, conflicts, writers-1)
	}
}

func TestUnifiedStateDoesNotLoseConcurrentSessionOrCheckpointWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state.json")
	const writers = 24
	sessionStores := make([]*storage.SessionStore, writers)
	checkpointStores := make([]*storage.CoordinatorCheckpointStore, writers)
	for index := 0; index < writers; index++ {
		var err error
		sessionStores[index], err = storage.OpenSessionStore(path)
		if err != nil {
			t.Fatalf("open session store %d: %v", index, err)
		}
		checkpointStores[index], err = storage.OpenCoordinatorCheckpointStore(path)
		if err != nil {
			t.Fatalf("open checkpoint store %d: %v", index, err)
		}
	}
	wantCheckpoint := coordinator.Checkpoint{Initialized: true, NextUpdateID: 500}
	start := make(chan struct{})
	errorsFound := make(chan error, writers*2)
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		index := index
		candidate := mustStartingSession(
			t,
			domain.SessionID(fmt.Sprintf("session-%d", index)),
			domain.IntentID(fmt.Sprintf("intent-%d", index)),
		)
		group.Add(2)
		go func() {
			defer group.Done()
			<-start
			_, _, err := sessionStores[index].PutStartingIfAbsent(
				context.Background(),
				candidate,
			)
			errorsFound <- err
		}()
		go func() {
			defer group.Done()
			<-start
			_, err := checkpointStores[index].Save(context.Background(), 0, wantCheckpoint)
			errorsFound <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent unified-state write error = %v", err)
		}
	}

	reopenedSessions, err := storage.OpenSessionStore(path)
	if err != nil {
		t.Fatalf("reopen sessions: %v", err)
	}
	for index := 0; index < writers; index++ {
		intentID := domain.IntentID(fmt.Sprintf("intent-%d", index))
		if _, found, err := reopenedSessions.GetByIntent(context.Background(), intentID); err != nil || !found {
			t.Fatalf("session %q after concurrent writes = found %v, error %v", intentID, found, err)
		}
	}
	reopenedCheckpoint, err := storage.OpenCoordinatorCheckpointStore(path)
	if err != nil {
		t.Fatalf("reopen checkpoint: %v", err)
	}
	got, found, err := reopenedCheckpoint.Load(context.Background())
	if err != nil || !found || got.Revision != 1 || !reflect.DeepEqual(got.Checkpoint, wantCheckpoint) {
		t.Fatalf("checkpoint after concurrent writes = (%#v, %v, %v), want revision 1 %#v", got, found, err, wantCheckpoint)
	}
}

func fullCheckpoint(phase coordinator.OutboundPhase) coordinator.Checkpoint {
	return coordinator.Checkpoint{
		Initialized:  true,
		NextUpdateID: 42,
		Blocked:      &coordinator.BlockedUpdate{UpdateID: 44, Reason: "unsupported authorized update"},
		Outbound: &coordinator.Outbound{
			OperationID: "status:42",
			UpdateID:    42,
			Status: coordinator.Status{
				ConversationID: 123,
				Text:           "safe status",
			},
			Phase: phase,
		},
	}
}

func providerBinding() domain.ProviderBinding {
	return domain.ProviderBinding{
		Provider:   domain.ProviderCodex,
		SessionID:  "provider-session-1",
		Generation: 1,
	}
}
