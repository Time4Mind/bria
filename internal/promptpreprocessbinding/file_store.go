package promptpreprocessbinding

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
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
)

// Mode is the durable topology selected when a request is admitted.
type Mode = promptpreprocess.Mode

const (
	ModeShared     = promptpreprocess.ModeShared
	ModePerSession = promptpreprocess.ModePerSession
)

// DesiredState is the durable lifecycle state of a hidden satellite.
type DesiredState string

const (
	DesiredActive   DesiredState = "active"
	DesiredArchived DesiredState = "archived"
)

type BindingKey struct {
	Mode      Mode
	SessionID domain.SessionID
}

// Binding retains the provider thread identity without exposing a satellite as
// a user-visible session.
type Binding struct {
	Key               BindingKey
	Desired           DesiredState
	ProviderSessionID string
}

// Store makes each lifecycle mutation durable before returning.
type Store interface {
	Load(context.Context, BindingKey) (Binding, bool, error)
	Save(context.Context, Binding) error
	List(context.Context) ([]Binding, error)
	Delete(context.Context, BindingKey) error
}

func validDesired(desired DesiredState) bool {
	return desired == DesiredActive || desired == DesiredArchived
}

const (
	bindingDocumentVersion   = 1
	maxBindingDocumentBytes  = 1 << 20
	maxBindingRecords        = 8192
	maxPrimaryIDBytes        = 256
	maxProviderThreadIDBytes = 64
)

var ErrBindingStore = errors.New("satellite binding store is unavailable")

type bindingDocument struct {
	Version  int             `json:"version"`
	Bindings []bindingRecord `json:"bindings"`
}

type bindingRecord struct {
	Mode              Mode         `json:"mode"`
	SessionID         string       `json:"session_id,omitempty"`
	Desired           DesiredState `json:"desired"`
	ProviderSessionID string       `json:"provider_session_id,omitempty"`
}

// FileBindingStore is the production hidden-satellite store. Every operation
// rereads one bounded strict document while holding the process-wide path lock;
// every mutation is temp-written with mode 0600, fsynced, atomically renamed,
// directory-fsynced and verified by a fresh read before it returns.
type FileBindingStore struct {
	path string
	mu   *sync.Mutex
}

var fileBindingLocks sync.Map

var _ Store = (*FileBindingStore)(nil)

func OpenFileBindingStore(path string) (*FileBindingStore, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, ErrBindingStore
	}
	path = filepath.Clean(path)
	lockValue, _ := fileBindingLocks.LoadOrStore(path, &sync.Mutex{})
	store := &FileBindingStore{path: path, mu: lockValue.(*sync.Mutex)}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateBindingDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if err := writeBindingDocument(path, bindingDocument{Version: bindingDocumentVersion, Bindings: []bindingRecord{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("%w: inspect binding file", ErrBindingStore)
	}
	if _, err := readBindingDocument(path); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileBindingStore) Load(ctx context.Context, key BindingKey) (Binding, bool, error) {
	if store == nil || store.mu == nil || ctx == nil || ctx.Err() != nil || validateBindingKey(key) != nil {
		return Binding{}, false, ErrBindingStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := readBindingDocument(store.path)
	if err != nil {
		return Binding{}, false, err
	}
	for _, record := range document.Bindings {
		binding := bindingFromRecord(record)
		if binding.Key == key {
			return binding, true, nil
		}
	}
	return Binding{}, false, nil
}

func (store *FileBindingStore) Save(ctx context.Context, binding Binding) error {
	if store == nil || store.mu == nil || ctx == nil || ctx.Err() != nil || validateBinding(binding) != nil {
		return ErrBindingStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := readBindingDocument(store.path)
	if err != nil {
		return err
	}
	next := recordFromBinding(binding)
	replaced := false
	for index := range document.Bindings {
		if bindingFromRecord(document.Bindings[index]).Key == binding.Key {
			document.Bindings[index] = next
			replaced = true
			break
		}
	}
	if !replaced {
		if len(document.Bindings) >= maxBindingRecords {
			return ErrBindingStore
		}
		document.Bindings = append(document.Bindings, next)
	}
	sortBindingRecords(document.Bindings)
	if err := writeBindingDocument(store.path, document); err != nil {
		return err
	}
	verified, err := readBindingDocument(store.path)
	if err != nil || !reflect.DeepEqual(verified, document) {
		return ErrBindingStore
	}
	return nil
}

func (store *FileBindingStore) List(ctx context.Context) ([]Binding, error) {
	if store == nil || store.mu == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrBindingStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := readBindingDocument(store.path)
	if err != nil {
		return nil, err
	}
	result := make([]Binding, 0, len(document.Bindings))
	for _, record := range document.Bindings {
		result = append(result, bindingFromRecord(record))
	}
	return result, nil
}

func (store *FileBindingStore) Delete(ctx context.Context, key BindingKey) error {
	if store == nil || store.mu == nil || ctx == nil || ctx.Err() != nil || validateBindingKey(key) != nil {
		return ErrBindingStore
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := readBindingDocument(store.path)
	if err != nil {
		return err
	}
	next := document.Bindings[:0]
	for _, record := range document.Bindings {
		if bindingFromRecord(record).Key != key {
			next = append(next, record)
		}
	}
	if len(next) == len(document.Bindings) {
		return nil
	}
	document.Bindings = append([]bindingRecord(nil), next...)
	if err := writeBindingDocument(store.path, document); err != nil {
		return err
	}
	verified, err := readBindingDocument(store.path)
	if err != nil || !reflect.DeepEqual(verified, document) {
		return ErrBindingStore
	}
	return nil
}

func readBindingDocument(path string) (bindingDocument, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || before.Size() > maxBindingDocumentBytes {
		return bindingDocument{}, ErrBindingStore
	}
	file, err := os.Open(path)
	if err != nil {
		return bindingDocument{}, ErrBindingStore
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBindingDocumentBytes+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || len(data) > maxBindingDocumentBytes || !os.SameFile(before, after) {
		return bindingDocument{}, ErrBindingStore
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document bindingDocument
	if err := decoder.Decode(&document); err != nil {
		return bindingDocument{}, ErrBindingStore
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return bindingDocument{}, ErrBindingStore
	}
	if err := validateBindingDocument(document); err != nil {
		return bindingDocument{}, err
	}
	return document, nil
}

func validateBindingDocument(document bindingDocument) error {
	if document.Version != bindingDocumentVersion || document.Bindings == nil || len(document.Bindings) > maxBindingRecords {
		return ErrBindingStore
	}
	seen := make(map[BindingKey]struct{}, len(document.Bindings))
	for _, record := range document.Bindings {
		binding := bindingFromRecord(record)
		if err := validateBinding(binding); err != nil {
			return err
		}
		if _, duplicate := seen[binding.Key]; duplicate {
			return ErrBindingStore
		}
		seen[binding.Key] = struct{}{}
	}
	return nil
}

func validateBinding(binding Binding) error {
	if err := validateBindingKey(binding.Key); err != nil || !validDesired(binding.Desired) {
		return ErrBindingStore
	}
	if binding.Key.Mode == ModeShared && binding.Desired != DesiredActive {
		return ErrBindingStore
	}
	if binding.ProviderSessionID != "" && !ValidProviderThreadID(binding.ProviderSessionID) {
		return ErrBindingStore
	}
	return nil
}

func validateBindingKey(key BindingKey) error {
	switch key.Mode {
	case ModeShared:
		if key.SessionID != "" {
			return ErrBindingStore
		}
	case ModePerSession:
		if !validBindingID(string(key.SessionID), maxPrimaryIDBytes) {
			return ErrBindingStore
		}
	default:
		return ErrBindingStore
	}
	return nil
}

func validBindingID(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

// ValidProviderThreadID validates the provider identity shared by persistence
// and transport adapters.
func ValidProviderThreadID(value string) bool {
	if len(value) < 1 || len(value) > maxProviderThreadIDBytes {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func recordFromBinding(binding Binding) bindingRecord {
	return bindingRecord{
		Mode: binding.Key.Mode, SessionID: string(binding.Key.SessionID), Desired: binding.Desired,
		ProviderSessionID: binding.ProviderSessionID,
	}
}

func bindingFromRecord(record bindingRecord) Binding {
	return Binding{
		Key:     BindingKey{Mode: record.Mode, SessionID: domain.SessionID(record.SessionID)},
		Desired: record.Desired, ProviderSessionID: record.ProviderSessionID,
	}
}

func sortBindingRecords(records []bindingRecord) {
	sort.Slice(records, func(left, right int) bool {
		if records[left].Mode != records[right].Mode {
			return records[left].Mode < records[right].Mode
		}
		return records[left].SessionID < records[right].SessionID
	})
}

func writeBindingDocument(path string, document bindingDocument) (returnErr error) {
	if err := validateBindingDocument(document); err != nil {
		return err
	}
	data, err := json.Marshal(document)
	if err != nil || len(data) > maxBindingDocumentBytes {
		return ErrBindingStore
	}
	directory := filepath.Dir(path)
	if err := validateBindingDirectory(directory); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return ErrBindingStore
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrBindingStore
	}
	temporary, err := os.CreateTemp(directory, ".bria-satellite-bindings-")
	if err != nil {
		return ErrBindingStore
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = ErrBindingStore
			}
		}
		if removeErr := os.Remove(temporaryPath); returnErr == nil && removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = ErrBindingStore
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrBindingStore
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		return ErrBindingStore
	}
	if err := temporary.Sync(); err != nil {
		return ErrBindingStore
	}
	if err := temporary.Close(); err != nil {
		return ErrBindingStore
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return ErrBindingStore
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return ErrBindingStore
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	if syncErr != nil || closeErr != nil {
		return ErrBindingStore
	}
	return nil
}

func validateBindingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrBindingStore
	}
	return nil
}
