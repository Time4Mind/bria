package authflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fileStoreVersion = 1
	maxFileStoreSize = 8 << 20
)

var authFileLocks sync.Map

type fileState struct {
	Version   int                       `json:"version"`
	Records   map[string]Record         `json:"records"`
	Deletions map[string]DeletionIntent `json:"deletions,omitempty"`
}

func (store *FileStore) LoadDeletionIntent(ctx context.Context, operationID string) (DeletionIntent, bool, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || normalized(operationID) == "" {
		return DeletionIntent{}, false, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return DeletionIntent{}, false, err
	}
	intent, found := state.Deletions[operationID]
	return intent, found, nil
}

func (store *FileStore) SaveDeletionIntent(ctx context.Context, intent DeletionIntent) (DeletionIntent, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || validateDeletionIntent(intent) != nil {
		return DeletionIntent{}, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return DeletionIntent{}, err
	}
	if current, found := state.Deletions[intent.OperationID]; found {
		if !sameDeletionBinding(current, intent) || current.Deletion == DeletionConfirmed && intent.Deletion != DeletionConfirmed {
			return DeletionIntent{}, ErrOperationConflict
		}
	}
	state.Deletions[intent.OperationID] = intent
	if err := writeFileState(store.path, state); err != nil {
		return DeletionIntent{}, err
	}
	verified, err := readFileState(store.path)
	if err != nil || verified.Deletions[intent.OperationID] != intent {
		return DeletionIntent{}, ErrStateUnavailable
	}
	return intent, nil
}

func (store *FileStore) ListPending(ctx context.Context, ownerID, privateChatID int64) ([]Record, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || ownerID <= 0 || privateChatID <= 0 {
		return nil, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0)
	for _, record := range state.Records {
		if record.OwnerID == ownerID && record.PrivateChatID == privateChatID && !terminal(record.Status) {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].OperationID < records[right].OperationID })
	return records, nil
}

func (store *FileStore) FindSubmissionByMessage(ctx context.Context, ownerID, privateChatID, messageID int64) (Record, bool, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || ownerID <= 0 || privateChatID <= 0 || messageID <= 0 {
		return Record{}, false, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return Record{}, false, err
	}
	var match Record
	found := false
	for _, record := range state.Records {
		if record.OwnerID != ownerID || record.PrivateChatID != privateChatID ||
			record.SecretMessageReference != (SecretMessageReference{ChatID: privateChatID, MessageID: messageID}) {
			continue
		}
		if found {
			return Record{}, false, ErrOperationConflict
		}
		match, found = record, true
	}
	return match, found, nil
}

func (store *FileStore) FindDeletionIntentByMessage(ctx context.Context, actorID, chatID, messageID int64) (DeletionIntent, bool, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || actorID <= 0 || chatID <= 0 || messageID <= 0 {
		return DeletionIntent{}, false, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return DeletionIntent{}, false, err
	}
	var match DeletionIntent
	found := false
	for _, intent := range state.Deletions {
		if intent.ActorID != actorID || intent.ChatID != chatID || intent.MessageID != messageID {
			continue
		}
		if found {
			return DeletionIntent{}, false, ErrOperationConflict
		}
		match, found = intent, true
	}
	return match, found, nil
}

// FileStore is the production durable replay fence. Every successful mutation
// is written to a protected temporary file, fsynced, atomically renamed,
// directory-fsynced and reread before the operation returns.
type FileStore struct {
	path string
}

func OpenFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, ErrStateUnavailable
	}
	path = filepath.Clean(path)
	store := &FileStore{path: path}
	lock := authFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	if _, err := readFileState(path); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileStore) Create(ctx context.Context, record Record) (Record, bool, error) {
	if store == nil || store.path == "" || ctx.Err() != nil {
		return Record{}, false, ErrStateUnavailable
	}
	record.Revision = 1
	if err := validateRecord(record); err != nil {
		return Record{}, false, err
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return Record{}, false, err
	}
	if current, exists := state.Records[record.OperationID]; exists {
		return current, false, nil
	}
	state.Records[record.OperationID] = record
	if err := writeFileState(store.path, state); err != nil {
		return Record{}, false, err
	}
	verified, err := readFileState(store.path)
	if err != nil || !reflect.DeepEqual(verified.Records[record.OperationID], record) {
		return Record{}, false, ErrStateUnavailable
	}
	return record, true, nil
}

func (store *FileStore) Load(ctx context.Context, operationID string) (Record, bool, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || normalized(operationID) == "" {
		return Record{}, false, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return Record{}, false, err
	}
	record, exists := state.Records[operationID]
	return record, exists, nil
}

func (store *FileStore) CompareAndSwap(ctx context.Context, operationID string, revision uint64, next Record) (Record, bool, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || normalized(operationID) == "" {
		return Record{}, false, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return Record{}, false, err
	}
	current, exists := state.Records[operationID]
	if !exists || current.Revision != revision {
		return current, false, nil
	}
	next.OperationID = operationID
	next.Revision = revision + 1
	if err := validateRecord(next); err != nil {
		return Record{}, false, err
	}
	if !sameDurableBinding(current, next) {
		return Record{}, false, ErrOperationConflict
	}
	state.Records[operationID] = next
	if err := writeFileState(store.path, state); err != nil {
		return Record{}, false, err
	}
	verified, err := readFileState(store.path)
	if err != nil || !reflect.DeepEqual(verified.Records[operationID], next) {
		return Record{}, false, ErrStateUnavailable
	}
	return next, true, nil
}

func (store *FileStore) PruneTerminalBefore(ctx context.Context, before time.Time) (int, error) {
	if store == nil || store.path == "" || ctx.Err() != nil || before.IsZero() {
		return 0, ErrStateUnavailable
	}
	lock := authFileLock(store.path)
	lock.Lock()
	defer lock.Unlock()
	state, err := readFileState(store.path)
	if err != nil {
		return 0, err
	}
	removed := 0
	for operationID, record := range state.Records {
		if prunable(record) && record.UpdatedAt.Before(before) {
			delete(state.Records, operationID)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := writeFileState(store.path, state); err != nil {
		return 0, err
	}
	verified, err := readFileState(store.path)
	if err != nil || !reflect.DeepEqual(verified, state) {
		return 0, ErrStateUnavailable
	}
	return removed, nil
}

func authFileLock(path string) *sync.Mutex {
	value, _ := authFileLocks.LoadOrStore(path, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func readFileState(path string) (fileState, error) {
	state := fileState{Version: fileStoreVersion, Records: make(map[string]Record), Deletions: make(map[string]DeletionIntent)}
	pathInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil || !pathInfo.Mode().IsRegular() {
		return fileState{}, ErrStateUnavailable
	}
	file, err := os.Open(path)
	if err != nil {
		return fileState{}, ErrStateUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileStoreSize {
		return fileState{}, ErrStateUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileStoreSize+1))
	if err != nil || len(data) > maxFileStoreSize {
		return fileState{}, ErrStateUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return fileState{}, ErrStateUnavailable
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileState{}, ErrStateUnavailable
	}
	if state.Version != fileStoreVersion || state.Records == nil {
		return fileState{}, ErrStateUnavailable
	}
	if state.Deletions == nil {
		state.Deletions = make(map[string]DeletionIntent)
	}
	for operationID, record := range state.Records {
		if operationID != record.OperationID || validateRecord(record) != nil {
			return fileState{}, ErrStateUnavailable
		}
	}
	for operationID, intent := range state.Deletions {
		if operationID != intent.OperationID || validateDeletionIntent(intent) != nil {
			return fileState{}, ErrStateUnavailable
		}
	}
	return state, nil
}

func writeFileState(path string, state fileState) (returnErr error) {
	data, err := json.Marshal(state)
	if err != nil || len(data) > maxFileStoreSize {
		return ErrStateUnavailable
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrStateUnavailable
	}
	temporary, err := os.CreateTemp(directory, ".authflow-*")
	if err != nil {
		return ErrStateUnavailable
	}
	temporaryPath := temporary.Name()
	open := true
	defer func() {
		if open {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrStateUnavailable
	}
	if _, err := temporary.Write(data); err != nil {
		return ErrStateUnavailable
	}
	if err := temporary.Sync(); err != nil {
		return ErrStateUnavailable
	}
	if err := temporary.Close(); err != nil {
		return ErrStateUnavailable
	}
	open = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return ErrStateUnavailable
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return ErrStateUnavailable
	}
	if err := directoryHandle.Sync(); err != nil {
		_ = directoryHandle.Close()
		return ErrStateUnavailable
	}
	if err := directoryHandle.Close(); err != nil {
		return ErrStateUnavailable
	}
	return nil
}

func validateRecord(record Record) error {
	if normalized(record.OperationID) == "" || len(record.OperationID) > 256 || record.Revision == 0 ||
		record.OwnerID <= 0 || record.PrivateChatID <= 0 || normalized(record.ComputerID) == "" || len(record.ComputerID) > 256 ||
		!record.Provider.valid() || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return ErrStateUnavailable
	}
	switch record.Status {
	case StatusStarting, StatusAwaitingAction, StatusCompleting, StatusAuthenticated, StatusRejected, StatusExpired:
	default:
		return ErrStateUnavailable
	}
	switch record.Deletion {
	case DeletionNotRequired, DeletionPending, DeletionConfirmed, DeletionUnconfirmed:
	default:
		return ErrStateUnavailable
	}
	if len(record.ChallengeReference) > 1024 || len(record.SubmissionOperationID) > 256 {
		return ErrStateUnavailable
	}
	if (record.SecretMessageReference.ChatID == 0) != (record.SecretMessageReference.MessageID == 0) ||
		record.SecretMessageReference.ChatID < 0 || record.SecretMessageReference.MessageID < 0 {
		return ErrStateUnavailable
	}
	if record.UpdatedAt.Before(record.CreatedAt) {
		return ErrStateUnavailable
	}
	hasChallenge := normalized(record.ChallengeReference) != "" && !record.ExpiresAt.IsZero()
	if (normalized(record.ChallengeReference) == "") != record.ExpiresAt.IsZero() {
		return ErrStateUnavailable
	}
	hasMessageReference := record.SecretMessageReference.ChatID > 0 && record.SecretMessageReference.MessageID > 0
	hasSubmission := normalized(record.SubmissionOperationID) != "" && hasMessageReference
	if (normalized(record.SubmissionOperationID) == "") != !hasMessageReference {
		return ErrStateUnavailable
	}
	if hasSubmission == (record.Deletion == DeletionNotRequired) {
		return ErrStateUnavailable
	}
	switch record.Status {
	case StatusStarting:
		if hasChallenge || hasSubmission {
			return ErrStateUnavailable
		}
	case StatusAwaitingAction:
		if !hasChallenge || hasSubmission {
			return ErrStateUnavailable
		}
	case StatusCompleting, StatusAuthenticated:
		if !hasChallenge || !hasSubmission {
			return ErrStateUnavailable
		}
	case StatusExpired:
		if !hasChallenge {
			return ErrStateUnavailable
		}
	case StatusRejected:
		if hasChallenge != hasSubmission {
			return ErrStateUnavailable
		}
	}
	return nil
}

func prunable(record Record) bool {
	return terminal(record.Status) && (record.Deletion == DeletionConfirmed || record.Deletion == DeletionNotRequired)
}

func sameDurableBinding(current, next Record) bool {
	return current.OperationID == next.OperationID && current.OwnerID == next.OwnerID &&
		current.PrivateChatID == next.PrivateChatID && current.ComputerID == next.ComputerID &&
		current.Provider == next.Provider && current.CreatedAt.Equal(next.CreatedAt)
}

func (state fileState) String() string {
	return fmt.Sprintf("authflow file state version %d with %d records", state.Version, len(state.Records))
}
