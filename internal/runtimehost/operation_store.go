package runtimehost

import "sync"

type OperationState string

const (
	OperationPending   OperationState = "pending"
	OperationCompleted OperationState = "completed"
)

type OperationRecord struct {
	Fingerprint string         `json:"fingerprint"`
	State       OperationState `json:"state"`
	Result      Result         `json:"result"`
	Error       string         `json:"error,omitempty"`
}

// OperationStore persists the intent before Submit acknowledges it. A pending
// record left by a crash has an unknown outcome and must never be re-executed
// automatically with the same operation ID.
type OperationStore interface {
	CreatePending(operationID, fingerprint string) (OperationRecord, bool, error)
	Complete(operationID, fingerprint string, result Result, executionError error) error
	Lookup(operationID string) (OperationRecord, bool, error)
}

type MemoryOperationStore struct {
	mu      sync.Mutex
	entries map[string]OperationRecord
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{entries: make(map[string]OperationRecord)}
}

func (s *MemoryOperationStore) CreatePending(
	operationID string,
	fingerprint string,
) (OperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.entries[operationID]
	if ok {
		if record.Fingerprint != fingerprint {
			return OperationRecord{}, false, ErrOperationIDConflict
		}
		return record, false, nil
	}
	record = OperationRecord{Fingerprint: fingerprint, State: OperationPending}
	s.entries[operationID] = record
	return record, true, nil
}

func (s *MemoryOperationStore) Complete(
	operationID string,
	fingerprint string,
	result Result,
	executionError error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.entries[operationID]
	if !ok || record.Fingerprint != fingerprint || record.State != OperationPending {
		return ErrOperationIDConflict
	}
	record.State = OperationCompleted
	record.Result = result
	if executionError != nil {
		record.Error = executionError.Error()
	}
	s.entries[operationID] = record
	return nil
}

func (s *MemoryOperationStore) Lookup(operationID string) (OperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.entries[operationID]
	return record, ok, nil
}
