package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
)

var ErrRevisionConflict = errors.New("settings revision conflict")

// Snapshot is one immutable settings value and its CAS revision. Revision zero
// identifies product defaults that have not been persisted yet.
type Snapshot struct {
	Revision uint64
	Settings Settings
}

type MemoryStore struct {
	mu    sync.Mutex
	state Snapshot
}

var _ VersionedStore = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{state: Snapshot{Settings: Default()}}
}

func (store *MemoryStore) Load(ctx context.Context) (Settings, error) {
	snapshot, err := store.Current(ctx)
	return snapshot.Settings, err
}

func (store *MemoryStore) Current(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.state, nil
}

func (store *MemoryStore) Update(ctx context.Context, fn func(*Settings) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("update function is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	next := store.state.Settings
	if err := fn(&next); err != nil {
		return err
	}
	_, err := store.compareAndSwapLocked(store.state.Revision, next)
	return err
}

func (store *MemoryStore) CompareAndSwap(ctx context.Context, expected uint64, next Settings) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return store.compareAndSwapLocked(expected, next)
}

func (store *MemoryStore) compareAndSwapLocked(expected uint64, next Settings) (Snapshot, error) {
	if store.state.Revision != expected {
		return Snapshot{}, ErrRevisionConflict
	}
	if expected == math.MaxUint64 {
		return Snapshot{}, errors.New("settings revision overflow")
	}
	if err := next.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate settings: %w", err)
	}
	store.state = Snapshot{Revision: expected + 1, Settings: next}
	return store.state, nil
}

type FileStore struct {
	mu            sync.Mutex
	fileMu        *sync.Mutex
	path          string
	state         Snapshot
	lastReloadErr error
}

var _ ReloadableStore = (*FileStore)(nil)

var settingsFileLocks sync.Map

func OpenFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("settings path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve settings path: %w", err)
	}
	lock, _ := settingsFileLocks.LoadOrStore(absolute, &sync.Mutex{})
	store := &FileStore{
		fileMu: lock.(*sync.Mutex), path: absolute,
		state: Snapshot{Settings: Default()},
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	snapshot, found, err := readSnapshotFile(absolute)
	if err != nil {
		return nil, err
	}
	if found {
		store.state = snapshot
	}
	return store, nil
}

func (store *FileStore) Load(ctx context.Context) (Settings, error) {
	snapshot, err := store.Current(ctx)
	return snapshot.Settings, err
}

func (store *FileStore) Current(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.state, nil
}

// Reload applies a complete valid local-file edit. An edit based on the active
// revision is committed as the next revision. Invalid and stale edits are
// observable but never replace the last valid active settings.
func (store *FileStore) Reload(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return store.reloadLocked()
}

func (store *FileStore) LastReloadError() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.lastReloadErr
}

func (store *FileStore) Update(ctx context.Context, fn func(*Settings) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("update function is required")
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := store.reloadLocked(); err != nil {
		return err
	}
	next := store.state.Settings
	if err := fn(&next); err != nil {
		return err
	}
	_, err := store.commitLocked(store.state.Revision, next)
	return err
}

func (store *FileStore) CompareAndSwap(ctx context.Context, expected uint64, next Settings) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.fileMu.Lock()
	defer store.fileMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if _, err := store.reloadLocked(); err != nil {
		return Snapshot{}, err
	}
	return store.commitLocked(expected, next)
}

func (store *FileStore) reloadLocked() (Snapshot, error) {
	disk, found, err := readSnapshotFile(store.path)
	if err != nil {
		store.lastReloadErr = err
		return store.state, err
	}
	if !found {
		if store.state.Revision == 0 {
			store.lastReloadErr = nil
			return store.state, nil
		}
		err := errors.New("settings file disappeared")
		store.lastReloadErr = err
		return store.state, err
	}
	if reflect.DeepEqual(disk, store.state) {
		store.lastReloadErr = nil
		return store.state, nil
	}
	if disk.Revision < store.state.Revision {
		store.lastReloadErr = ErrRevisionConflict
		return store.state, ErrRevisionConflict
	}
	if disk.Revision == store.state.Revision {
		return store.commitLocked(store.state.Revision, disk.Settings)
	}
	store.state = disk
	store.lastReloadErr = nil
	return store.state, nil
}

func (store *FileStore) commitLocked(expected uint64, next Settings) (Snapshot, error) {
	if store.state.Revision != expected {
		return Snapshot{}, ErrRevisionConflict
	}
	if expected == math.MaxUint64 {
		return Snapshot{}, errors.New("settings revision overflow")
	}
	if err := next.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("validate settings: %w", err)
	}
	want := Snapshot{Revision: expected + 1, Settings: next}
	if err := writeSnapshotFile(store.path, want); err != nil {
		store.lastReloadErr = err
		return Snapshot{}, fmt.Errorf("persist settings: %w", err)
	}
	persisted, found, err := readSnapshotFile(store.path)
	if err != nil {
		store.lastReloadErr = err
		return Snapshot{}, fmt.Errorf("reread settings: %w", err)
	}
	if !found || !reflect.DeepEqual(persisted, want) {
		err := errors.New("reread settings: persisted value mismatch")
		store.lastReloadErr = err
		return Snapshot{}, err
	}
	store.state = persisted
	store.lastReloadErr = nil
	return store.state, nil
}

func readSnapshotFile(path string) (Snapshot, bool, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("inspect settings file: %w", err)
	}
	if err := validateSettingsFileInfo(before); err != nil {
		return Snapshot{}, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("open settings: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("verify settings file: %w", err)
	}
	if !os.SameFile(before, after) {
		return Snapshot{}, false, errors.New("settings file changed during secure open")
	}
	if err := validateSettingsFileInfo(after); err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := Decode(file)
	if err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, true, nil
}

func validateSettingsFileInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("settings file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("settings file must be regular")
	}
	permissions := info.Mode().Perm()
	if permissions&0o600 != 0o600 {
		return errors.New("settings file owner must have read and write access")
	}
	if runtime.GOOS != "windows" && permissions&0o022 != 0 {
		return errors.New("settings file must not be writable by group or others")
	}
	return nil
}

func writeSnapshotFile(path string, snapshot Snapshot) error {
	data, err := json.MarshalIndent(documentFromSnapshot(snapshot), "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".settings-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err == nil {
		err = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return err
}
