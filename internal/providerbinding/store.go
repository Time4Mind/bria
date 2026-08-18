// Package providerbinding records the provider identity emitted by a hook
// running inside one exact Bria tmux window.
package providerbinding

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

const (
	EnvNodeID      = "BRIA_BINDING_NODE_ID"
	EnvSessionID   = "BRIA_BINDING_SESSION_ID"
	EnvTmuxSession = "BRIA_BINDING_TMUX_SESSION"
	EnvTmuxWindow  = "BRIA_BINDING_TMUX_WINDOW"
)

type Record struct {
	NodeID            string    `json:"node_id"`
	SessionID         string    `json:"session_id"`
	ProviderSessionID string    `json:"provider_session_id"`
	Workdir           string    `json:"workdir"`
	TmuxSession       string    `json:"tmux_session"`
	TmuxWindow        string    `json:"tmux_window"`
	UpdatedAt         time.Time `json:"updated_at"`
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
	records[bindingKey(record.NodeID, record.SessionID)] = record
	encoded, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provider bindings: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".provider-bindings-*")
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
	if err := os.Rename(temporaryPath, s.path); err != nil {
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
	if len(data) > 1<<20 {
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
	return nil
}

func bindingKey(nodeID, sessionID string) string { return nodeID + ":" + sessionID }
