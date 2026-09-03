package nodelink

import (
	"context"
	"sync"
)

// MemoryOperationLedger is useful for single-process composition and tests. A
// production multi-computer composition supplies a durable implementation.
type MemoryOperationLedger struct {
	mu         sync.Mutex
	operations map[string]string
}

var _ OperationLedger = (*MemoryOperationLedger)(nil)

func NewMemoryOperationLedger() *MemoryOperationLedger {
	return &MemoryOperationLedger{operations: make(map[string]string)}
}

func (ledger *MemoryOperationLedger) ApplyOnce(ctx context.Context, operation Operation, apply func() error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if digest, exists := ledger.operations[operation.ID]; exists {
		if digest != operation.Digest {
			return false, ErrOperationConflict
		}
		return true, nil
	}
	if err := apply(); err != nil {
		return false, err
	}
	ledger.operations[operation.ID] = operation.Digest
	return false, nil
}
