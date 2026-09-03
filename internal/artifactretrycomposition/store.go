package artifactretrycomposition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bria/internal/artifactdelivery"
	"bria/internal/domain"
)

const (
	storeVersion   = 1
	maxStoreSize   = int64(16 << 20)
	maxRecords     = 10_000
	stateIssued    = "issued"
	stateExecuting = "executing"
	stateComplete  = "complete"
)

type retryRecord struct {
	Binding          Binding                  `json:"binding"`
	Descriptor       string                   `json:"descriptor,omitempty"`
	Summary          artifactdelivery.Summary `json:"summary"`
	State            string                   `json:"state"`
	ClaimOperationID string                   `json:"claim_operation_id,omitempty"`
	Published        bool                     `json:"published,omitempty"`
	PriorBinding     *Binding                 `json:"prior_binding,omitempty"`
	PriorClaimID     string                   `json:"prior_claim_id,omitempty"`
}

type storeState struct {
	Version int                    `json:"version"`
	Records map[string]retryRecord `json:"records"`
}

type fileStore struct {
	path  string
	state storeState
}

func openFileStore(path string) (*fileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidConfiguration
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	store := &fileStore{path: absolute, state: storeState{Version: storeVersion, Records: make(map[string]retryRecord)}}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > maxStoreSize {
		return nil, ErrInvalidConfiguration
	}
	handle, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	decoder := json.NewDecoder(io.LimitReader(handle, maxStoreSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidConfiguration
	}
	if err := validateState(store.state); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *fileStore) put(record retryRecord) error {
	if store == nil || !validRecord(record) {
		return ErrInvalidConfiguration
	}
	if _, found := store.state.Records[record.Binding.FinalOperationID]; !found && len(store.state.Records) >= maxRecords {
		return ErrInvalidConfiguration
	}
	next := cloneState(store.state)
	next.Records[record.Binding.FinalOperationID] = record
	data, err := json.Marshal(next)
	if err != nil || int64(len(data)) > maxStoreSize {
		return ErrInvalidConfiguration
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".artifact-retry-")
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
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	if err := directoryHandle.Sync(); err != nil {
		_ = directoryHandle.Close()
		return err
	}
	if err := directoryHandle.Close(); err != nil {
		return err
	}
	store.state = next
	return nil
}

func validateState(state storeState) error {
	if state.Version != storeVersion || state.Records == nil || len(state.Records) > maxRecords {
		return ErrInvalidConfiguration
	}
	for id, record := range state.Records {
		if id != record.Binding.FinalOperationID || !validRecord(record) {
			return ErrInvalidConfiguration
		}
	}
	return nil
}

func validRecord(record retryRecord) bool {
	if record.Binding.SessionID == "" || record.Binding.MessageID == "" || record.Binding.FinalOperationID != record.Binding.MessageID+":final" ||
		!utf8.ValidString(record.Binding.MessageID) || len(record.Binding.MessageID) > 1024 || len(record.Binding.FinalOperationID) > 1030 ||
		record.Summary.Total < 0 || record.Summary.Confirmed < 0 || record.Summary.Unconfirmed < 0 ||
		record.Summary.Confirmed+record.Summary.Unconfirmed != record.Summary.Total || record.Summary.FinalID != record.Binding.FinalOperationID {
		return false
	}
	switch record.State {
	case stateComplete:
		return record.Descriptor == "" && record.Binding.ExpiresAt.IsZero() && record.ClaimOperationID == "" && validPrior(record)
	case stateIssued:
		return validBinding(record.Binding) && validDescriptor(record) && record.ClaimOperationID == "" && record.Summary.NeedsExplicitRetry && validPrior(record)
	case stateExecuting:
		return validBinding(record.Binding) && validDescriptor(record) && record.ClaimOperationID != "" && record.Summary.NeedsExplicitRetry && record.PriorBinding == nil && record.PriorClaimID == ""
	default:
		return false
	}
}

func validPrior(record retryRecord) bool {
	if record.PriorBinding == nil {
		return record.PriorClaimID == ""
	}
	return record.PriorClaimID != "" && validBinding(*record.PriorBinding) && record.PriorBinding.FinalOperationID == record.Binding.FinalOperationID &&
		record.PriorBinding.Generation < record.Binding.Generation
}

func validDescriptor(record retryRecord) bool {
	return record.Descriptor != "" && strings.TrimSpace(record.Descriptor) == record.Descriptor && len(record.Descriptor) <= 4096 &&
		record.Binding.ExpiresAt.Unix() > 0 && record.Binding.ExpiresAt.Nanosecond() == 0
}

func cloneState(state storeState) storeState {
	clone := storeState{Version: state.Version, Records: make(map[string]retryRecord, len(state.Records))}
	for id, record := range state.Records {
		if record.PriorBinding != nil {
			binding := *record.PriorBinding
			record.PriorBinding = &binding
		}
		clone.Records[id] = record
	}
	return clone
}

func presentationID(binding Binding) domain.SessionID {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%d", binding.SessionID, binding.MessageID, binding.FinalOperationID, binding.Generation, binding.Slot)))
	bytes := append([]byte(nil), digest[:16]...)
	bytes[6] = bytes[6]&0x0f | 0x50
	bytes[8] = bytes[8]&0x3f | 0x80
	encoded := hex.EncodeToString(bytes)
	return domain.SessionID(encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:])
}
