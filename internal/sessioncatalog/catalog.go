// Package sessioncatalog persists one origin-neutral archive of provider
// sessions discovered on explicitly configured computers.
package sessioncatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"bria/internal/domain"
	"bria/internal/sessiondiscovery"
)

const formatVersion = 1
const maxDocumentBytes = int64(16 << 20)

var (
	ErrInvalidCatalog  = errors.New("invalid discovered session catalog")
	ErrDiscoveryFailed = errors.New("session discovery did not complete")
	catalogLocks       sync.Map
)

// Entry gives a provider session a stable Bria identity without changing its
// exact provider binding or grouping it by the application that created it.
type Entry struct {
	ID     domain.SessionID        `json:"id"`
	Record sessiondiscovery.Record `json:"record"`
}

// ArchivedSession projects a discovered provider session into the existing
// navigation and exact-resume domain model. Generation one is the durable
// baseline; the first Bria-managed resume must return generation two while
// retaining Record.ProviderSessionID exactly.
func (entry Entry) ArchivedSession() (domain.Session, error) {
	validated := sessiondiscovery.Merge(entry.Record)
	if len(validated.Rejections) != 0 || len(validated.Records) != 1 || entry.ID != stableSessionID(entry.Record) {
		return domain.Session{}, ErrInvalidCatalog
	}
	record := validated.Records[0]
	return domain.RestoreSession(domain.SessionSnapshot{
		ID: entry.ID, IntentID: domain.IntentID("discovered:" + string(entry.ID)),
		ComputerID: record.ComputerID, Provider: record.Provider, Workdir: record.Workdir,
		Status: domain.SessionArchived,
		Binding: &domain.ProviderBinding{
			Provider: record.Provider, SessionID: record.ProviderSessionID, Generation: 1,
		},
		CreatedAt: record.CreatedAt, StateChangedAt: record.UpdatedAt,
		Lifetime: domain.SessionLifetimeNever,
	})
}

type document struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// FileStore is a durable, atomic archive. All instances opened for one path
// serialize reload-and-replace operations inside this process.
type FileStore struct {
	path string
	mu   *sync.Mutex
}

func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, ErrInvalidCatalog
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrInvalidCatalog
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, ErrInvalidCatalog
	}
	canonical := filepath.Join(parent, filepath.Base(absolute))
	muValue, _ := catalogLocks.LoadOrStore(canonical, &sync.Mutex{})
	store := &FileStore{path: canonical, mu: muValue.(*sync.Mutex)}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := readDocument(store.path); err != nil {
		return nil, err
	}
	return store, nil
}

// Synchronize discovers every source before taking the durable replacement
// lock. Any source error, invalid record, or ambiguity leaves the archive
// unchanged. Successful discovery adds or refreshes records but never removes
// an older archive entry merely because a provider no longer lists it.
func (store *FileStore) Synchronize(ctx context.Context, sources ...sessiondiscovery.Source) ([]Entry, error) {
	if store == nil || store.mu == nil || ctx == nil {
		return nil, ErrInvalidCatalog
	}
	report, discoveryErr := sessiondiscovery.DiscoverAll(ctx, sources...)
	if discoveryErr != nil || len(report.Rejections) != 0 {
		reasons := make([]error, 0, len(report.Rejections)+2)
		reasons = append(reasons, ErrDiscoveryFailed, discoveryErr)
		for _, rejection := range report.Rejections {
			reasons = append(reasons, rejection.Err)
		}
		return nil, errors.Join(reasons...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := readDocument(store.path)
	if err != nil {
		return nil, err
	}
	records := make([]sessiondiscovery.Record, 0, len(current.Entries)+len(report.Records))
	for _, entry := range current.Entries {
		records = append(records, entry.Record)
	}
	records = append(records, report.Records...)
	merged := sessiondiscovery.Merge(records...)
	if len(merged.Rejections) != 0 {
		reasons := []error{ErrDiscoveryFailed}
		for _, rejection := range merged.Rejections {
			reasons = append(reasons, rejection.Err)
		}
		return nil, errors.Join(reasons...)
	}
	next, err := entriesFor(merged.Records)
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(current.Entries, next) {
		return cloneEntries(next), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := writeDocument(store.path, document{Version: formatVersion, Entries: next}); err != nil {
		return nil, err
	}
	verified, err := readDocument(store.path)
	if err != nil || !reflect.DeepEqual(verified.Entries, next) {
		return nil, errors.Join(ErrInvalidCatalog, err)
	}
	return cloneEntries(next), nil
}

func (store *FileStore) Entries(ctx context.Context) ([]Entry, error) {
	if store == nil || store.mu == nil || ctx == nil {
		return nil, ErrInvalidCatalog
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	loaded, err := readDocument(store.path)
	if err != nil {
		return nil, err
	}
	return cloneEntries(loaded.Entries), nil
}

// ArchivedSessions exposes the durable catalog through the domain type used
// by Telegram session and archive navigation.
func (store *FileStore) ArchivedSessions(ctx context.Context) ([]domain.Session, error) {
	entries, err := store.Entries(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]domain.Session, 0, len(entries))
	for _, entry := range entries {
		session, err := entry.ArchivedSession()
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

func entriesFor(records []sessiondiscovery.Record) ([]Entry, error) {
	entries := make([]Entry, 0, len(records))
	seenIDs := make(map[domain.SessionID]struct{}, len(records))
	for _, record := range records {
		id := stableSessionID(record)
		if _, exists := seenIDs[id]; exists {
			return nil, ErrInvalidCatalog
		}
		seenIDs[id] = struct{}{}
		entries = append(entries, Entry{ID: id, Record: record})
	}
	return entries, nil
}

func stableSessionID(record sessiondiscovery.Record) domain.SessionID {
	hash := sha256.New()
	writeHashPart(hash, "bria-discovered-session-v1")
	writeHashPart(hash, string(record.ComputerID))
	writeHashPart(hash, string(record.Provider))
	writeHashPart(hash, record.ProviderSessionID)
	sum := hash.Sum(nil)[:16]
	// UUIDv8 reserves the variant/version bits while leaving the remaining
	// bytes available for Bria's SHA-256 based stable identity mapping.
	sum[6] = sum[6]&0x0f | 0x80
	sum[8] = sum[8]&0x3f | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], sum[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], sum[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], sum[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], sum[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], sum[10:16])
	return domain.SessionID(encoded)
}

type hashWriter interface {
	Write([]byte) (int, error)
}

func writeHashPart(writer hashWriter, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write([]byte(value))
}

func readDocument(path string) (document, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return document{Version: formatVersion, Entries: []Entry{}}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxDocumentBytes {
		return document{}, ErrInvalidCatalog
	}
	file, err := os.Open(path)
	if err != nil {
		return document{}, ErrInvalidCatalog
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return document{}, ErrInvalidCatalog
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxDocumentBytes+1))
	decoder.DisallowUnknownFields()
	var loaded document
	if err := decoder.Decode(&loaded); err != nil {
		return document{}, ErrInvalidCatalog
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return document{}, ErrInvalidCatalog
	}
	if loaded.Version != formatVersion || loaded.Entries == nil {
		return document{}, ErrInvalidCatalog
	}
	records := make([]sessiondiscovery.Record, 0, len(loaded.Entries))
	for _, entry := range loaded.Entries {
		records = append(records, entry.Record)
	}
	merged := sessiondiscovery.Merge(records...)
	if len(merged.Rejections) != 0 || len(merged.Records) != len(loaded.Entries) {
		return document{}, ErrInvalidCatalog
	}
	expected, err := entriesFor(merged.Records)
	if err != nil || !reflect.DeepEqual(expected, loaded.Entries) {
		return document{}, ErrInvalidCatalog
	}
	return document{Version: formatVersion, Entries: cloneEntries(loaded.Entries)}, nil
}

func writeDocument(path string, next document) error {
	encoded, err := json.Marshal(next)
	if err != nil {
		return ErrInvalidCatalog
	}
	encoded = append(encoded, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".session-catalog-*")
	if err != nil {
		return fmt.Errorf("create session catalog candidate: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect session catalog candidate: %w", err)
	}
	if _, err := io.Copy(temporary, bytes.NewReader(encoded)); err != nil {
		return fmt.Errorf("write session catalog candidate: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush session catalog candidate: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session catalog candidate: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace session catalog: %w", err)
	}
	committed = true
	dir, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open session catalog directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("flush session catalog directory: %w", err)
	}
	return nil
}

func cloneEntries(entries []Entry) []Entry {
	return append([]Entry(nil), entries...)
}
