package nodelink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bria/internal/domain"
)

var (
	ErrInvalidPersistentState = errors.New("invalid node-link persistent state")
	ErrOperationInDoubt       = errors.New("operation outcome is unknown after interrupted commit")
)

const MaxPairingFileBytes = 4 << 20

type PairingFile struct {
	mu       sync.RWMutex
	path     string
	snapshot PairingSnapshot
}

func OpenPairingFile(path string) (*PairingFile, error) {
	store := &PairingFile{path: path}
	if err := validateFilePath(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err == nil && info.Size() > MaxPairingFileBytes {
		return nil, ErrInvalidPersistentState
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > MaxPairingFileBytes {
		return nil, ErrInvalidPersistentState
	}
	var snapshot PairingSnapshot
	if err := decodeFile(data, &snapshot); err != nil {
		return nil, err
	}
	registry, err := RestorePairingRegistry(snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPersistentState, err)
	}
	store.snapshot = registry.Snapshot()
	return store, nil
}

func (store *PairingFile) Save(snapshot PairingSnapshot) error {
	if store == nil {
		return ErrInvalidPersistentState
	}
	registry, err := RestorePairingRegistry(snapshot)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPersistentState, err)
	}
	canonical := registry.Snapshot()
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.saveLocked(canonical)
}

func (store *PairingFile) saveLocked(canonical PairingSnapshot) error {
	if len(canonical.Pending) > 1024 || len(canonical.Grants) > 4096 || len(canonical.Consumed) > 8192 {
		return ErrInvalidPersistentState
	}
	encoded, err := json.Marshal(canonical)
	if err != nil || len(encoded) > MaxPairingFileBytes {
		return ErrInvalidPersistentState
	}
	if err := atomicNodeJSON(store.path, canonical); err != nil {
		return err
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		return err
	}
	var verified PairingSnapshot
	if err := decodeFile(data, &verified); err != nil {
		return err
	}
	verifiedRegistry, err := RestorePairingRegistry(verified)
	if err != nil {
		return err
	}
	store.snapshot = verifiedRegistry.Snapshot()
	return nil
}

func (store *PairingFile) PruneExpired(now time.Time) error {
	if store == nil || now.IsZero() {
		return ErrInvalidPersistentState
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	registry, err := RestorePairingRegistry(store.snapshot)
	if err != nil {
		return err
	}
	snapshot := registry.Snapshot()
	pending := snapshot.Pending[:0]
	for _, challenge := range snapshot.Pending {
		if now.Before(challenge.ExpiresAt) {
			pending = append(pending, challenge)
		}
	}
	snapshot.Pending = pending
	consumed := snapshot.Consumed[:0]
	for _, tombstone := range snapshot.Consumed {
		if now.Before(tombstone.RetainUntil) {
			consumed = append(consumed, tombstone)
		}
	}
	snapshot.Consumed = consumed
	registry, err = RestorePairingRegistry(snapshot)
	if err != nil {
		return err
	}
	return store.saveLocked(registry.Snapshot())
}

func (store *PairingFile) PendingCount() int {
	if store == nil {
		return 0
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.snapshot.Pending)
}

func (store *PairingFile) Issue(challenge PairingChallenge, now time.Time) error {
	if store == nil {
		return ErrInvalidPersistentState
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	registry, err := RestorePairingRegistry(store.snapshot)
	if err != nil {
		return err
	}
	if err := registry.Issue(challenge, now); err != nil {
		return err
	}
	return store.saveLocked(registry.Snapshot())
}

func (store *PairingFile) Confirm(id, code, fingerprint string, now time.Time) (PairingGrant, error) {
	if store == nil {
		return PairingGrant{}, ErrInvalidPersistentState
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	registry, err := RestorePairingRegistry(store.snapshot)
	if err != nil {
		return PairingGrant{}, err
	}
	grant, err := registry.Confirm(id, code, fingerprint, now)
	if err != nil {
		return PairingGrant{}, err
	}
	if err := store.saveLocked(registry.Snapshot()); err != nil {
		return PairingGrant{}, err
	}
	return grant, nil
}

func (store *PairingFile) Revoke(id domain.ComputerID) error {
	if store == nil {
		return ErrInvalidPersistentState
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	registry, err := RestorePairingRegistry(store.snapshot)
	if err != nil {
		return err
	}
	if err := registry.Revoke(id); err != nil {
		return err
	}
	return store.saveLocked(registry.Snapshot())
}

func (store *PairingFile) Authorized(id domain.ComputerID, fingerprint string) bool {
	if store == nil {
		return false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	registry, err := RestorePairingRegistry(store.snapshot)
	return err == nil && registry.Authorized(id, fingerprint)
}

func (store *PairingFile) ComputerForFingerprint(fingerprint string) (domain.ComputerID, bool) {
	if store == nil {
		return "", false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	registry, err := RestorePairingRegistry(store.snapshot)
	if err != nil {
		return "", false
	}
	return registry.ComputerForFingerprint(fingerprint)
}

func (store *PairingFile) Snapshot() PairingSnapshot {
	if store == nil {
		return PairingSnapshot{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	registry, _ := RestorePairingRegistry(store.snapshot)
	return registry.Snapshot()
}

type ledgerPhase string

const (
	ledgerPending ledgerPhase = "pending"
	ledgerApplied ledgerPhase = "applied"
)

type ledgerEntry struct {
	ID     string      `json:"id"`
	Digest string      `json:"digest"`
	Phase  ledgerPhase `json:"phase"`
}

type ledgerSnapshot struct {
	Version uint16        `json:"version"`
	Entries []ledgerEntry `json:"entries"`
}

type FileOperationLedger struct {
	mu      sync.Mutex
	path    string
	entries map[string]ledgerEntry
}

type OperationResolution string

const (
	OperationApplied    OperationResolution = "applied"
	OperationNotApplied OperationResolution = "not_applied"
)

var _ OperationLedger = (*FileOperationLedger)(nil)

func OpenFileOperationLedger(path string) (*FileOperationLedger, error) {
	if err := validateFilePath(path); err != nil {
		return nil, err
	}
	ledger := &FileOperationLedger{path: path, entries: make(map[string]ledgerEntry)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ledger, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot ledgerSnapshot
	if err := decodeFile(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != ProtocolVersion {
		return nil, ErrIncompatibleVersion
	}
	for _, entry := range snapshot.Entries {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Digest) == "" || (entry.Phase != ledgerPending && entry.Phase != ledgerApplied) {
			return nil, ErrInvalidPersistentState
		}
		if _, duplicate := ledger.entries[entry.ID]; duplicate {
			return nil, ErrInvalidPersistentState
		}
		ledger.entries[entry.ID] = entry
	}
	return ledger, nil
}

func (ledger *FileOperationLedger) ApplyOnce(ctx context.Context, operation Operation, apply func() error) (bool, error) {
	if ledger == nil || apply == nil || strings.TrimSpace(operation.ID) == "" || strings.TrimSpace(operation.Digest) == "" {
		return false, ErrInvalidPersistentState
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if entry, exists := ledger.entries[operation.ID]; exists {
		if entry.Digest != operation.Digest {
			return false, ErrOperationConflict
		}
		if entry.Phase == ledgerPending {
			return false, ErrOperationInDoubt
		}
		return true, nil
	}
	ledger.entries[operation.ID] = ledgerEntry{ID: operation.ID, Digest: operation.Digest, Phase: ledgerPending}
	if err := ledger.persist(); err != nil {
		delete(ledger.entries, operation.ID)
		return false, err
	}
	if err := apply(); err != nil {
		delete(ledger.entries, operation.ID)
		if persistErr := ledger.persist(); persistErr != nil {
			return false, errors.Join(err, persistErr)
		}
		return false, err
	}
	entry := ledger.entries[operation.ID]
	entry.Phase = ledgerApplied
	ledger.entries[operation.ID] = entry
	if err := ledger.persist(); err != nil {
		entry.Phase = ledgerPending
		ledger.entries[operation.ID] = entry
		return false, errors.Join(ErrOperationInDoubt, err)
	}
	return false, nil
}

// InDoubtOperations returns effects which may have run but whose final ledger
// commit was interrupted. Callers must reconcile each operation against the
// operation-specific external state. They must not infer an outcome from a
// transport error or retry it automatically.
func (ledger *FileOperationLedger) InDoubtOperations() []Operation {
	if ledger == nil {
		return nil
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	operations := make([]Operation, 0)
	for _, entry := range ledger.entries {
		if entry.Phase == ledgerPending {
			operations = append(operations, Operation{ID: entry.ID, Digest: entry.Digest})
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].ID < operations[j].ID })
	return operations
}

// Resolve records the result of an explicit reconciliation. OperationApplied
// closes the idempotency key permanently; OperationNotApplied permits a later
// ApplyOnce. The caller owns the operation-specific probe or manual decision.
func (ledger *FileOperationLedger) Resolve(ctx context.Context, operation Operation, resolution OperationResolution) error {
	if ledger == nil || strings.TrimSpace(operation.ID) == "" || strings.TrimSpace(operation.Digest) == "" || (resolution != OperationApplied && resolution != OperationNotApplied) {
		return ErrInvalidPersistentState
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	entry, exists := ledger.entries[operation.ID]
	if !exists {
		return ErrInvalidPersistentState
	}
	if entry.Digest != operation.Digest {
		return ErrOperationConflict
	}
	if entry.Phase == ledgerApplied {
		if resolution == OperationApplied {
			return nil
		}
		return ErrOperationConflict
	}
	previous := entry
	if resolution == OperationApplied {
		entry.Phase = ledgerApplied
		ledger.entries[operation.ID] = entry
	} else {
		delete(ledger.entries, operation.ID)
	}
	if err := ledger.persist(); err != nil {
		ledger.entries[operation.ID] = previous
		return err
	}
	return nil
}

func (ledger *FileOperationLedger) persist() error {
	ids := make([]string, 0, len(ledger.entries))
	for id := range ledger.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	snapshot := ledgerSnapshot{Version: ProtocolVersion, Entries: make([]ledgerEntry, 0, len(ids))}
	for _, id := range ids {
		snapshot.Entries = append(snapshot.Entries, ledger.entries[id])
	}
	if err := atomicNodeJSON(ledger.path, snapshot); err != nil {
		return err
	}
	data, err := os.ReadFile(ledger.path)
	if err != nil {
		return err
	}
	var verified ledgerSnapshot
	if err := decodeFile(data, &verified); err != nil {
		return err
	}
	if verified.Version != ProtocolVersion || len(verified.Entries) != len(snapshot.Entries) {
		return ErrInvalidPersistentState
	}
	return nil
}

func validateFilePath(path string) error {
	if strings.TrimSpace(path) == "" || filepath.Base(path) == "." || filepath.Base(path) == string(filepath.Separator) {
		return ErrInvalidPersistentState
	}
	return nil
}

func decodeFile(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidPersistentState, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidPersistentState
	}
	return nil
}

func atomicNodeJSON(path string, value any) error {
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
	defer os.Remove(temporaryPath)
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
		return ErrInvalidPersistentState
	}
	return nil
}
