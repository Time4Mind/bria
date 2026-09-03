// Package telegramops persists opaque Telegram operation ledgers. Semantic
// validation belongs to telegramflow; this package owns only bounded atomic
// storage, deterministic listing, and phase compare-and-swap.
package telegramops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	Version = 1
	maxSize = 16 << 20
	maxList = 100
)

func StatusSequence(operationID string) (uint64, error) {
	prefix := "status:"
	if strings.HasPrefix(operationID, "recovery:callback:") {
		prefix = "recovery:callback:"
	} else if !strings.HasPrefix(operationID, prefix) {
		return 0, errors.New("durable status operation ID must contain its update sequence")
	}
	sequence, err := strconv.ParseUint(strings.TrimPrefix(operationID, prefix), 10, 64)
	if err != nil || sequence == 0 {
		return 0, errors.New("durable status operation sequence is invalid")
	}
	return sequence, nil
}

var ErrExists = errors.New("Telegram operation already exists")

type Namespace string

const (
	Callbacks Namespace = "operations"
	Statuses  Namespace = "statuses"
)

type Snapshot struct {
	Version    int                        `json:"version"`
	Operations map[string]json.RawMessage `json:"operations"`
	Statuses   map[string]json.RawMessage `json:"statuses,omitempty"`
}

type Store interface {
	Load(context.Context, Namespace, string) (json.RawMessage, bool, error)
	Insert(context.Context, Namespace, string, json.RawMessage) error
	CompareAndSwap(context.Context, Namespace, string, string, json.RawMessage) (bool, error)
	List(context.Context, Namespace, []string, int) ([]json.RawMessage, error)
	Snapshot(context.Context) (Snapshot, error)
}

type FileStore struct {
	mu            sync.Mutex
	path          string
	syncDirectory func(string) error
	state         Snapshot
}

func (store *FileStore) SetDirectorySync(syncDirectory func(string) error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.syncDirectory = syncDirectory
}

func OpenFile(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("Telegram operation store path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	store := &FileStore{path: absolute, syncDirectory: syncDir, state: emptySnapshot()}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return nil, errors.New("Telegram operation store must be a bounded regular file")
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("Telegram operation store contains trailing JSON")
	}
	if err := validateSnapshot(store.state); err != nil {
		return nil, err
	}
	if store.state.Statuses == nil {
		store.state.Statuses = make(map[string]json.RawMessage)
	}
	return store, nil
}

func NewMemory() Store { return &memoryStore{state: emptySnapshot()} }

func emptySnapshot() Snapshot {
	return Snapshot{Version: Version, Operations: make(map[string]json.RawMessage), Statuses: make(map[string]json.RawMessage)}
}

func (store *FileStore) Load(ctx context.Context, namespace Namespace, id string) (json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if store == nil || id == "" || !validNamespace(namespace) {
		return nil, false, errors.New("Telegram operation identity is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, ok := namespaceMap(store.state, namespace)[id]
	return cloneRaw(raw), ok, nil
}

func (store *FileStore) Insert(ctx context.Context, namespace Namespace, id string, raw json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || id == "" || !validNamespace(namespace) || !json.Valid(raw) {
		return errors.New("Telegram operation is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := namespaceMap(store.state, namespace)[id]; ok {
		return ErrExists
	}
	next := cloneSnapshot(store.state)
	namespaceMap(next, namespace)[id] = cloneRaw(raw)
	if err := writeAtomic(store.path, next, store.syncDirectory); err != nil {
		return err
	}
	store.state = next
	return nil
}

func (store *FileStore) CompareAndSwap(ctx context.Context, namespace Namespace, id, oldPhase string, raw json.RawMessage) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil || id == "" || oldPhase == "" || !validNamespace(namespace) || !json.Valid(raw) {
		return false, errors.New("Telegram operation transition is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, ok := namespaceMap(store.state, namespace)[id]
	if !ok || rawPhase(current) != oldPhase {
		return false, nil
	}
	next := cloneSnapshot(store.state)
	namespaceMap(next, namespace)[id] = cloneRaw(raw)
	if err := writeAtomic(store.path, next, store.syncDirectory); err != nil {
		return false, err
	}
	store.state = next
	return true, nil
}

func (store *FileStore) List(ctx context.Context, namespace Namespace, phases []string, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || !validNamespace(namespace) || limit < 1 || limit > maxList {
		return nil, errors.New("Telegram operation list limit must be 1..100")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return list(namespaceMap(store.state, namespace), phases, limit), nil
}

func (store *FileStore) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if store == nil {
		return Snapshot{}, errors.New("Telegram operation store is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneSnapshot(store.state), nil
}

// ReopenSnapshot rereads the last directory-synced physical file rather than
// trusting the process-local copy.
func (store *FileStore) ReopenSnapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if store == nil {
		return Snapshot{}, errors.New("Telegram operation store is required")
	}
	reopened, err := OpenFile(store.path)
	if err != nil {
		return Snapshot{}, err
	}
	return reopened.Snapshot(ctx)
}

type memoryStore struct {
	mu    sync.Mutex
	state Snapshot
}

func (store *memoryStore) Load(ctx context.Context, namespace Namespace, id string) (json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if store == nil || id == "" || !validNamespace(namespace) {
		return nil, false, errors.New("Telegram operation identity is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	raw, ok := namespaceMap(store.state, namespace)[id]
	return cloneRaw(raw), ok, nil
}
func (store *memoryStore) Insert(ctx context.Context, namespace Namespace, id string, raw json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || id == "" || !validNamespace(namespace) || !json.Valid(raw) {
		return errors.New("Telegram operation is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	target := namespaceMap(store.state, namespace)
	if _, ok := target[id]; ok {
		return ErrExists
	}
	target[id] = cloneRaw(raw)
	return nil
}
func (store *memoryStore) CompareAndSwap(ctx context.Context, namespace Namespace, id, oldPhase string, raw json.RawMessage) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if store == nil || id == "" || oldPhase == "" || !validNamespace(namespace) || !json.Valid(raw) {
		return false, errors.New("Telegram operation transition is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	target := namespaceMap(store.state, namespace)
	current, ok := target[id]
	if !ok || rawPhase(current) != oldPhase {
		return false, nil
	}
	target[id] = cloneRaw(raw)
	return true, nil
}
func (store *memoryStore) List(ctx context.Context, namespace Namespace, phases []string, limit int) ([]json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || !validNamespace(namespace) || limit < 1 || limit > maxList {
		return nil, errors.New("Telegram operation list limit must be 1..100")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return list(namespaceMap(store.state, namespace), phases, limit), nil
}
func (store *memoryStore) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneSnapshot(store.state), nil
}

func list(records map[string]json.RawMessage, phases []string, limit int) []json.RawMessage {
	wanted := make(map[string]bool, len(phases))
	for _, phase := range phases {
		wanted[phase] = true
	}
	type item struct {
		id       string
		sequence uint64
		raw      json.RawMessage
	}
	items := make([]item, 0)
	for id, raw := range records {
		phase, sequence := rawPhase(raw), rawSequence(raw)
		if len(wanted) == 0 || wanted[phase] {
			items = append(items, item{id, sequence, cloneRaw(raw)})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].sequence == items[j].sequence {
			return items[i].id < items[j].id
		}
		return items[i].sequence < items[j].sequence
	})
	if len(items) > limit {
		items = items[:limit]
	}
	result := make([]json.RawMessage, len(items))
	for i := range items {
		result[i] = items[i].raw
	}
	return result
}

func rawPhase(raw json.RawMessage) string {
	var v struct {
		Phase string `json:"phase"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Phase
}
func rawSequence(raw json.RawMessage) uint64 {
	var v struct {
		Sequence uint64 `json:"sequence"`
		UpdateID int64  `json:"update_id"`
	}
	_ = json.Unmarshal(raw, &v)
	if v.Sequence > 0 {
		return v.Sequence
	}
	if v.UpdateID > 0 {
		return uint64(v.UpdateID)
	}
	return 0
}
func validNamespace(namespace Namespace) bool { return namespace == Callbacks || namespace == Statuses }
func namespaceMap(state Snapshot, namespace Namespace) map[string]json.RawMessage {
	if namespace == Callbacks {
		return state.Operations
	}
	return state.Statuses
}
func cloneRaw(raw json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), raw...) }
func cloneSnapshot(state Snapshot) Snapshot {
	clone := emptySnapshot()
	clone.Version = state.Version
	for id, raw := range state.Operations {
		clone.Operations[id] = cloneRaw(raw)
	}
	for id, raw := range state.Statuses {
		clone.Statuses[id] = cloneRaw(raw)
	}
	return clone
}
func validateSnapshot(state Snapshot) error {
	if state.Version != Version || state.Operations == nil {
		return errors.New("Telegram operation store schema is invalid")
	}
	for id, raw := range state.Operations {
		if id == "" || !json.Valid(raw) {
			return errors.New("callback operation record is invalid")
		}
	}
	for id, raw := range state.Statuses {
		if id == "" || !json.Valid(raw) {
			return errors.New("status operation record is invalid")
		}
	}
	return nil
}

func writeAtomic(path string, state Snapshot, syncDirectory func(string) error) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxSize {
		return errors.New("Telegram operation store exceeds size limit")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".telegram-operations-")
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
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync Telegram operation directory: %w", err)
	}
	return nil
}
func syncDir(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return err
	}
	return handle.Close()
}
