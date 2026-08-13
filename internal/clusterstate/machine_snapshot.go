package clusterstate

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Time4Mind/bria/internal/domain"
)

type snapshot struct {
	Version     int               `json:"version"`
	State       *domain.State     `json:"state"`
	Ledger      map[string]Result `json:"operation_ledger"`
	LedgerOrder []string          `json:"operation_order"`
}

func (m *Machine) remember(result Result) {
	m.ledger[result.OperationID] = result
	m.ledgerOrder = append(m.ledgerOrder, result.OperationID)
	for len(m.ledgerOrder) > m.ledgerLimit {
		oldest := m.ledgerOrder[0]
		m.ledgerOrder = m.ledgerOrder[1:]
		delete(m.ledger, oldest)
	}
}

func (m *Machine) MarshalSnapshot() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ledger := make(map[string]Result, len(m.ledger))
	for operationID, result := range m.ledger {
		ledger[operationID] = result
	}
	return json.Marshal(snapshot{
		Version:     1,
		State:       m.state.Clone(),
		Ledger:      ledger,
		LedgerOrder: append([]string(nil), m.ledgerOrder...),
	})
}

func (m *Machine) RestoreSnapshot(data []byte) error {
	restored, err := decodeSnapshot(data)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.installSnapshot(restored)
	return nil
}

func decodeSnapshot(data []byte) (snapshot, error) {
	var restored snapshot
	if err := json.Unmarshal(data, &restored); err != nil {
		return snapshot{}, fmt.Errorf("decode state snapshot: %w", err)
	}
	if restored.Version != 1 {
		return snapshot{}, fmt.Errorf("unsupported snapshot version: %d", restored.Version)
	}
	if restored.State == nil || restored.State.SchemaVersion != domain.StateSchemaVersion {
		return snapshot{}, errors.New("snapshot contains unsupported domain state")
	}
	if len(restored.LedgerOrder) > defaultLedgerLimit {
		return snapshot{}, errors.New("snapshot operation ledger exceeds limit")
	}
	if len(restored.Ledger) != len(restored.LedgerOrder) {
		return snapshot{}, errors.New("snapshot operation ledger is inconsistent")
	}
	seen := make(map[string]struct{}, len(restored.LedgerOrder))
	for _, operationID := range restored.LedgerOrder {
		result, ok := restored.Ledger[operationID]
		_, duplicate := seen[operationID]
		if !ok || duplicate || operationID == "" || result.OperationID != operationID {
			return snapshot{}, errors.New("snapshot operation order references invalid result")
		}
		seen[operationID] = struct{}{}
	}
	return restored, nil
}

func (m *Machine) installSnapshot(restored snapshot) {
	m.state = restored.State.Clone()
	m.ledger = make(map[string]Result, len(restored.Ledger))
	for operationID, result := range restored.Ledger {
		m.ledger[operationID] = result
	}
	m.ledgerOrder = append([]string(nil), restored.LedgerOrder...)
	m.ledgerLimit = defaultLedgerLimit
}

// InspectSnapshot validates a logical snapshot and returns a defensive copy of
// its domain state without mutating a running machine.
func InspectSnapshot(data []byte) (*domain.State, error) {
	machine := NewMachine(nil)
	if err := machine.RestoreSnapshot(data); err != nil {
		return nil, err
	}
	return machine.State(), nil
}
