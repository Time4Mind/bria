// Package interactionsourcestore durably remembers Telegram source messages
// consumed as secret provider answers without retaining their content.
package interactionsourcestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	fileVersion = 1
	maxBytes    = 4 << 20
	maxSources  = 10_000
)

var (
	ErrInvalid           = errors.New("invalid consumed interaction source")
	ErrImmutableIdentity = errors.New("consumed interaction source identity changed")
	ErrExhausted         = errors.New("consumed interaction source store capacity exhausted")
)

type Source struct {
	OperationID    string    `json:"operation_id"`
	ActorID        int64     `json:"actor_id"`
	ConversationID int64     `json:"conversation_id"`
	MessageID      int64     `json:"message_id"`
	DeletionKnown  bool      `json:"deletion_known"`
	Revision       uint64    `json:"revision"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Store interface {
	Load(context.Context, int64, int64, int64) (Source, bool, error)
	Record(context.Context, Source) (Source, error)
	ConfirmDeletion(context.Context, Source, uint64) (Source, bool, error)
}

type state struct {
	Version int               `json:"version"`
	Sources map[string]Source `json:"sources"`
}

type FileStore struct {
	mu    sync.Mutex
	path  string
	state state
}

func OpenFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	store := &FileStore{path: absolute, state: state{Version: fileVersion, Sources: make(map[string]Source)}}
	info, err := os.Stat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, ErrInvalid
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, fmt.Errorf("decode consumed interaction sources: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	if err := validateState(store.state); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileStore) Load(ctx context.Context, actorID, conversationID, messageID int64) (Source, bool, error) {
	if err := validateLookup(ctx, store, actorID, conversationID, messageID); err != nil {
		return Source{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	source, found := store.state.Sources[key(actorID, conversationID, messageID)]
	return source, found, nil
}

func (store *FileStore) Record(ctx context.Context, source Source) (Source, error) {
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	source.Revision = 1
	if store == nil || validate(source) != nil {
		return Source{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key(source.ActorID, source.ConversationID, source.MessageID)
	if current, found := store.state.Sources[id]; found {
		if sameIdentity(current, source) {
			return current, nil
		}
		return Source{}, ErrImmutableIdentity
	}
	if len(store.state.Sources) >= maxSources {
		return Source{}, ErrExhausted
	}
	next := cloneState(store.state)
	next.Sources[id] = source
	if err := write(store.path, next); err != nil {
		return Source{}, err
	}
	store.state = next
	return source, nil
}

func (store *FileStore) ConfirmDeletion(ctx context.Context, source Source, revision uint64) (Source, bool, error) {
	if err := ctx.Err(); err != nil {
		return Source{}, false, err
	}
	if store == nil || revision == 0 {
		return Source{}, false, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key(source.ActorID, source.ConversationID, source.MessageID)
	current, found := store.state.Sources[id]
	if !found || current.Revision != revision {
		return current, false, nil
	}
	if !sameIdentity(current, source) {
		return Source{}, false, ErrImmutableIdentity
	}
	if current.DeletionKnown {
		return current, true, nil
	}
	current.DeletionKnown = true
	current.Revision++
	current.UpdatedAt = source.UpdatedAt
	if validate(current) != nil {
		return Source{}, false, ErrInvalid
	}
	next := cloneState(store.state)
	next.Sources[id] = current
	if err := write(store.path, next); err != nil {
		return Source{}, false, err
	}
	store.state = next
	return current, true, nil
}

type MemoryStore struct {
	mu      sync.Mutex
	sources map[string]Source
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{sources: make(map[string]Source)} }

func (store *MemoryStore) Load(ctx context.Context, actorID, conversationID, messageID int64) (Source, bool, error) {
	if err := validateLookup(ctx, store, actorID, conversationID, messageID); err != nil {
		return Source{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	source, found := store.sources[key(actorID, conversationID, messageID)]
	return source, found, nil
}

func (store *MemoryStore) Record(ctx context.Context, source Source) (Source, error) {
	if err := ctx.Err(); err != nil {
		return Source{}, err
	}
	source.Revision = 1
	if store == nil || validate(source) != nil {
		return Source{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key(source.ActorID, source.ConversationID, source.MessageID)
	if current, found := store.sources[id]; found {
		if sameIdentity(current, source) {
			return current, nil
		}
		return Source{}, ErrImmutableIdentity
	}
	if len(store.sources) >= maxSources {
		return Source{}, ErrExhausted
	}
	store.sources[id] = source
	return source, nil
}

func (store *MemoryStore) ConfirmDeletion(ctx context.Context, source Source, revision uint64) (Source, bool, error) {
	if err := ctx.Err(); err != nil {
		return Source{}, false, err
	}
	if store == nil || revision == 0 {
		return Source{}, false, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	id := key(source.ActorID, source.ConversationID, source.MessageID)
	current, found := store.sources[id]
	if !found || current.Revision != revision {
		return current, false, nil
	}
	if !sameIdentity(current, source) {
		return Source{}, false, ErrImmutableIdentity
	}
	if current.DeletionKnown {
		return current, true, nil
	}
	current.DeletionKnown = true
	current.Revision++
	current.UpdatedAt = source.UpdatedAt
	if validate(current) != nil {
		return Source{}, false, ErrInvalid
	}
	store.sources[id] = current
	return current, true, nil
}

func validateLookup(ctx context.Context, store any, actorID, conversationID, messageID int64) error {
	if ctx == nil || store == nil || actorID <= 0 || conversationID <= 0 || messageID <= 0 {
		return ErrInvalid
	}
	return ctx.Err()
}

func validateState(current state) error {
	if current.Version != fileVersion || current.Sources == nil || len(current.Sources) > maxSources {
		return ErrInvalid
	}
	for id, source := range current.Sources {
		if id != key(source.ActorID, source.ConversationID, source.MessageID) || validate(source) != nil {
			return ErrInvalid
		}
	}
	return nil
}

func validate(source Source) error {
	if !saneText(source.OperationID, 256) || source.ActorID <= 0 || source.ConversationID <= 0 || source.MessageID <= 0 ||
		source.Revision == 0 || source.CreatedAt.IsZero() || source.UpdatedAt.Before(source.CreatedAt) {
		return ErrInvalid
	}
	return nil
}

func saneText(value string, max int) bool {
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func key(actorID, conversationID, messageID int64) string {
	return fmt.Sprintf("%d:%d:%d", actorID, conversationID, messageID)
}

func sameIdentity(left, right Source) bool {
	return left.OperationID == right.OperationID && left.ActorID == right.ActorID && left.ConversationID == right.ConversationID &&
		left.MessageID == right.MessageID && left.CreatedAt.Equal(right.CreatedAt)
}

func cloneState(current state) state {
	next := state{Version: current.Version, Sources: make(map[string]Source, len(current.Sources))}
	for id, source := range current.Sources {
		next.Sources[id] = source
	}
	return next
}

func write(path string, current state) error {
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil || len(data) > maxBytes {
		return ErrInvalid
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".interaction-sources-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
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
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

var _ Store = (*FileStore)(nil)
var _ Store = (*MemoryStore)(nil)
