package computer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"bria/internal/domain"
)

var ErrInvalidSnapshot = errors.New("invalid computer snapshot")

type CatalogFile struct {
	mu       sync.RWMutex
	path     string
	snapshot CatalogSnapshot
}

func OpenCatalogFile(path string) (*CatalogFile, error) {
	store := &CatalogFile{path: path}
	if err := validateStorePath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot CatalogSnapshot
	if err := decodeSnapshot(data, &snapshot); err != nil {
		return nil, err
	}
	catalog, err := RestoreCatalog(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	store.snapshot = catalog.Snapshot()
	return store, nil
}

func (store *CatalogFile) Save(snapshot CatalogSnapshot) error {
	if store == nil {
		return ErrInvalidSnapshot
	}
	catalog, err := RestoreCatalog(snapshot)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	canonical := catalog.Snapshot()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveLocked(canonical)
}

func (store *CatalogFile) saveLocked(canonical CatalogSnapshot) error {
	if err := atomicJSON(store.path, canonical); err != nil {
		return err
	}
	verified, err := readCatalogSnapshot(store.path)
	if err != nil {
		return err
	}
	store.snapshot = verified
	return nil
}

func (store *CatalogFile) Upsert(record Record) error {
	if store == nil {
		return ErrInvalidSnapshot
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	catalog, err := RestoreCatalog(store.snapshot)
	if err != nil {
		return err
	}
	if err := catalog.Upsert(record); err != nil {
		return err
	}
	return store.saveLocked(catalog.Snapshot())
}

func (store *CatalogFile) Lookup(id domain.ComputerID) (Record, bool) {
	if store == nil {
		return Record{}, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	catalog, err := RestoreCatalog(store.snapshot)
	if err != nil {
		return Record{}, false
	}
	return catalog.Lookup(id)
}

func (store *CatalogFile) Snapshot() CatalogSnapshot {
	if store == nil {
		return CatalogSnapshot{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	catalog, _ := RestoreCatalog(store.snapshot)
	return catalog.Snapshot()
}

type FenceFile struct {
	mu       sync.RWMutex
	path     string
	snapshot FenceSnapshot
}

func OpenFenceFile(path string) (*FenceFile, error) {
	store := &FenceFile{path: path}
	if err := validateStorePath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot FenceSnapshot
	if err := decodeSnapshot(data, &snapshot); err != nil {
		return nil, err
	}
	fence, err := RestoreFence(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	store.snapshot = fence.Snapshot()
	return store, nil
}

func (store *FenceFile) Save(snapshot FenceSnapshot) error {
	if store == nil {
		return ErrInvalidSnapshot
	}
	fence, err := RestoreFence(snapshot)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	canonical := fence.Snapshot()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveLocked(canonical)
}

func (store *FenceFile) saveLocked(canonical FenceSnapshot) error {
	if err := atomicJSON(store.path, canonical); err != nil {
		return err
	}
	verified, err := readFenceSnapshot(store.path)
	if err != nil {
		return err
	}
	store.snapshot = verified
	return nil
}

func (store *FenceFile) Accept(term CoordinatorTerm) error {
	if store == nil {
		return ErrInvalidSnapshot
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	fence, err := RestoreFence(store.snapshot)
	if err != nil {
		return err
	}
	if err := fence.Accept(term); err != nil {
		return err
	}
	return store.saveLocked(fence.Snapshot())
}

func (store *FenceFile) Validate(term CoordinatorTerm) error {
	if store == nil {
		return ErrNoCoordinator
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	fence, err := RestoreFence(store.snapshot)
	if err != nil {
		return err
	}
	return fence.Validate(term)
}

func (store *FenceFile) Snapshot() FenceSnapshot {
	if store == nil {
		return FenceSnapshot{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.snapshot
}

func readCatalogSnapshot(path string) (CatalogSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CatalogSnapshot{}, err
	}
	var snapshot CatalogSnapshot
	if err := decodeSnapshot(data, &snapshot); err != nil {
		return CatalogSnapshot{}, err
	}
	catalog, err := RestoreCatalog(snapshot)
	if err != nil {
		return CatalogSnapshot{}, err
	}
	return catalog.Snapshot(), nil
}

func readFenceSnapshot(path string) (FenceSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FenceSnapshot{}, err
	}
	var snapshot FenceSnapshot
	if err := decodeSnapshot(data, &snapshot); err != nil {
		return FenceSnapshot{}, err
	}
	fence, err := RestoreFence(snapshot)
	if err != nil {
		return FenceSnapshot{}, err
	}
	return fence.Snapshot(), nil
}

func validateStorePath(path string) error {
	if path == "" || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return ErrInvalidSnapshot
	}
	return nil
}

func decodeSnapshot(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidSnapshot
	}
	return nil
}

func atomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	defer cleanup()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	verified, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(verified, data) {
		return ErrInvalidSnapshot
	}
	return nil
}
