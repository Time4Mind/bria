// Package providerbinding records the provider identity emitted by a hook
// running inside one exact Bria tmux window.
package providerbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const (
	EnvNodeID            = "BRIA_BINDING_NODE_ID"
	EnvSessionID         = "BRIA_BINDING_SESSION_ID"
	EnvRuntimeGeneration = "BRIA_BINDING_RUNTIME_GENERATION"
	EnvTmuxSession       = "BRIA_BINDING_TMUX_SESSION"
	EnvTmuxWindow        = "BRIA_BINDING_TMUX_WINDOW"
)

type Record struct {
	NodeID            string    `json:"node_id"`
	SessionID         string    `json:"session_id"`
	ProviderSessionID string    `json:"provider_session_id"`
	Workdir           string    `json:"workdir"`
	TmuxSession       string    `json:"tmux_session"`
	TmuxWindow        string    `json:"tmux_window"`
	TmuxWindowID      string    `json:"tmux_window_id,omitempty"`
	TmuxPane          string    `json:"tmux_pane,omitempty"`
	RuntimeGeneration uint64    `json:"runtime_generation"`
	UpdatedAt         time.Time `json:"updated_at"`
}

const maxStoreBytes = 1 << 20

// SweepInput is a caller-owned lifecycle snapshot. KeepRefs must contain all
// live, starting, degraded, and resume-pending sessions. Archived entries are
// removable only when the caller has already confirmed their target is gone.
// A non-zero MissingBefore permits cleanup of records absent from that
// snapshot, but only when their last binding update predates the cutoff.
type SweepInput struct {
	KeepRefs      []domain.SessionRef
	Archived      []SweepArchived
	MissingBefore time.Time
}

type SweepArchived struct {
	Ref               domain.SessionRef
	RuntimeGeneration uint64
	TargetAbsent      bool
}

type Store struct{ path string }

func NewStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("provider binding path must be absolute")
	}
	return &Store{path: filepath.Clean(path)}, nil
}

func (s *Store) Lookup(ref domain.SessionRef, workdir string) (Record, bool, error) {
	record, ok, err := s.LookupRef(ref)
	if err != nil || !ok || filepath.Clean(record.Workdir) != filepath.Clean(workdir) {
		return Record{}, false, err
	}
	return record, true, nil
}

// LookupRef is used by Stop hooks after a provider-side cwd change. The exact
// Bria tmux identity and provider session id are checked by the caller before
// the original bound workdir is reused for transcript verification.
func (s *Store) LookupRef(ref domain.SessionRef) (Record, bool, error) {
	records, err := s.read()
	if err != nil {
		return Record{}, false, err
	}
	record, ok := records[bindingKey(string(ref.NodeID), string(ref.SessionID))]
	if !ok {
		return Record{}, false, nil
	}
	return record, true, nil
}

// Snapshot returns a validated value copy of every stored binding. The file
// lock covers the read so reconciliation can inspect exact refs and
// generations without racing a Put/Delete/Sweep transaction.
func (s *Store) Snapshot() ([]Record, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return nil, fmt.Errorf("create provider binding directory: %w", err)
	}
	lock, err := acquireFileLock(s.path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("open provider binding lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	records, err := s.read()
	if err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(records))
	for key, record := range records {
		if err := validateRecord(record); err != nil {
			return nil, fmt.Errorf("invalid provider binding %q: %w", key, err)
		}
		if key != bindingKey(record.NodeID, record.SessionID) {
			return nil, fmt.Errorf("provider binding key %q does not match record", key)
		}
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		return bindingKey(result[i].NodeID, result[i].SessionID) < bindingKey(result[j].NodeID, result[j].SessionID)
	})
	return result, nil
}

// List is an alias for Snapshot for reconciliation callers.
func (s *Store) List() ([]Record, error) { return s.Snapshot() }

func (s *Store) Put(record Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create provider binding directory: %w", err)
	}
	lock, err := acquireFileLock(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("open provider binding lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	records, err := s.read()
	if err != nil {
		return err
	}
	key := bindingKey(record.NodeID, record.SessionID)
	if existing, ok := records[key]; ok && existing.RuntimeGeneration > record.RuntimeGeneration {
		// A hook from an older provider process must not resurrect or replace a
		// newer binding. Equal generations remain valid because /new and /clear
		// happen inside the same provider process whose environment is unchanged.
		return nil
	}
	if existing, ok := records[key]; ok && existing.RuntimeGeneration == record.RuntimeGeneration &&
		existing.TmuxPane != "" && record.TmuxPane != "" && existing.TmuxPane != record.TmuxPane {
		return errors.New("provider binding generation is already owned by another tmux pane")
	}
	records[key] = record
	return writeAtomic(s.path, records)
}

// Delete removes the binding identified by ref. Missing bindings are a
// successful no-op so cleanup can safely be retried.
func (s *Store) Delete(ref domain.SessionRef) error {
	return s.deleteIf(ref, 0, false)
}

// DeleteIfGeneration removes ref when the stored process generation is at
// most the finalized generation. A missing binding or newer generation is a
// successful no-op, making late cleanup unable to remove a newer binding.
func (s *Store) DeleteIfGeneration(ref domain.SessionRef, generation uint64) error {
	return s.deleteIf(ref, generation, true)
}

func (s *Store) deleteIf(ref domain.SessionRef, generation uint64, conditional bool) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("invalid provider binding reference: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create provider binding directory: %w", err)
	}
	lock, err := acquireFileLock(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("open provider binding lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	records, err := s.read()
	if err != nil {
		return err
	}
	key := bindingKey(string(ref.NodeID), string(ref.SessionID))
	record, ok := records[key]
	if !ok || (conditional && record.RuntimeGeneration > generation) {
		return nil
	}
	delete(records, key)
	return writeAtomic(s.path, records)
}

// Sweep applies one caller-confirmed lifecycle snapshot atomically. It never
// infers liveness from omitted refs unless MissingBefore is supplied.
func (s *Store) Sweep(input SweepInput) error {
	keep := make(map[string]struct{}, len(input.KeepRefs))
	for _, ref := range input.KeepRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("invalid provider binding keep reference: %w", err)
		}
		keep[bindingKey(string(ref.NodeID), string(ref.SessionID))] = struct{}{}
	}
	archived := make(map[string]SweepArchived, len(input.Archived))
	for _, candidate := range input.Archived {
		if err := candidate.Ref.Validate(); err != nil {
			return fmt.Errorf("invalid provider binding archived reference: %w", err)
		}
		archived[bindingKey(string(candidate.Ref.NodeID), string(candidate.Ref.SessionID))] = candidate
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("create provider binding directory: %w", err)
	}
	lock, err := acquireFileLock(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("open provider binding lock: %w", err)
	}
	defer lock.Close() //nolint:errcheck
	records, err := s.read()
	if err != nil {
		return err
	}
	changed := false
	for key, record := range records {
		if _, ok := keep[key]; ok {
			continue
		}
		if candidate, ok := archived[key]; ok {
			if candidate.TargetAbsent && record.RuntimeGeneration <= candidate.RuntimeGeneration {
				delete(records, key)
				changed = true
			}
			continue
		}
		if !input.MissingBefore.IsZero() && record.UpdatedAt.Before(input.MissingBefore) {
			delete(records, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return writeAtomic(s.path, records)
}

func writeAtomic(path string, records map[string]Record) error {
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provider bindings: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxStoreBytes {
		return errors.New("provider bindings are too large")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".provider-bindings-*")
	if err != nil {
		return fmt.Errorf("create provider binding update: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("write provider bindings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync provider bindings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install provider bindings: %w", err)
	}
	return nil
}

func (s *Store) read() (map[string]Record, error) {
	info, err := os.Lstat(s.path)
	if os.IsNotExist(err) {
		return make(map[string]Record), nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect provider bindings: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("provider bindings must be a regular file")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("read provider bindings: %w", err)
	}
	if len(data) > maxStoreBytes {
		return nil, errors.New("provider bindings are too large")
	}
	records := make(map[string]Record)
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("decode provider bindings: %w", err)
	}
	return records, nil
}

func validateRecord(record Record) error {
	for label, value := range map[string]string{
		"node id": record.NodeID, "session id": record.SessionID,
		"provider session id": record.ProviderSessionID,
		"tmux session":        record.TmuxSession, "tmux window": record.TmuxWindow,
	} {
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\x00\r\n\t") {
			return fmt.Errorf("invalid provider binding %s", label)
		}
	}
	if !filepath.IsAbs(record.Workdir) || record.UpdatedAt.IsZero() {
		return errors.New("invalid provider binding workdir or timestamp")
	}
	if record.TmuxWindowID != "" && (!strings.HasPrefix(record.TmuxWindowID, "@") ||
		strings.ContainsAny(record.TmuxWindowID, "\x00\r\n\t")) {
		return errors.New("invalid provider binding tmux window id")
	}
	if record.TmuxPane != "" && (!strings.HasPrefix(record.TmuxPane, "%") ||
		strings.ContainsAny(record.TmuxPane, "\x00\r\n\t")) {
		return errors.New("invalid provider binding tmux pane")
	}
	return nil
}

func bindingKey(nodeID, sessionID string) string { return nodeID + ":" + sessionID }
