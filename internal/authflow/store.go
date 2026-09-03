package authflow

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe test and single-process adapter. Durable
// deployments provide the same CAS contract with atomic disk persistence.
type MemoryStore struct {
	mu        sync.RWMutex
	records   map[string]Record
	deletions map[string]DeletionIntent
}

func (store *MemoryStore) ListPending(ctx context.Context, ownerID, privateChatID int64) ([]Record, error) {
	if store == nil || ctx.Err() != nil || ownerID <= 0 || privateChatID <= 0 {
		return nil, ErrStateUnavailable
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := make([]Record, 0)
	for _, record := range store.records {
		if record.OwnerID == ownerID && record.PrivateChatID == privateChatID && !terminal(record.Status) {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].OperationID < records[right].OperationID })
	return records, nil
}

func (store *MemoryStore) FindSubmissionByMessage(ctx context.Context, ownerID, privateChatID, messageID int64) (Record, bool, error) {
	if store == nil || ctx.Err() != nil || ownerID <= 0 || privateChatID <= 0 || messageID <= 0 {
		return Record{}, false, ErrStateUnavailable
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var match Record
	found := false
	for _, record := range store.records {
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

func (store *MemoryStore) FindDeletionIntentByMessage(ctx context.Context, actorID, chatID, messageID int64) (DeletionIntent, bool, error) {
	if store == nil || ctx.Err() != nil || actorID <= 0 || chatID <= 0 || messageID <= 0 {
		return DeletionIntent{}, false, ErrStateUnavailable
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var match DeletionIntent
	found := false
	for _, intent := range store.deletions {
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

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), deletions: make(map[string]DeletionIntent)}
}

func (store *MemoryStore) LoadDeletionIntent(ctx context.Context, operationID string) (DeletionIntent, bool, error) {
	if store == nil || ctx.Err() != nil || normalized(operationID) == "" {
		return DeletionIntent{}, false, ErrStateUnavailable
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	intent, found := store.deletions[operationID]
	return intent, found, nil
}

func (store *MemoryStore) SaveDeletionIntent(ctx context.Context, intent DeletionIntent) (DeletionIntent, error) {
	if store == nil || ctx.Err() != nil || validateDeletionIntent(intent) != nil {
		return DeletionIntent{}, ErrStateUnavailable
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, found := store.deletions[intent.OperationID]; found {
		if !sameDeletionBinding(current, intent) || current.Deletion == DeletionConfirmed && intent.Deletion != DeletionConfirmed {
			return DeletionIntent{}, ErrOperationConflict
		}
	}
	store.deletions[intent.OperationID] = intent
	return intent, nil
}

func (store *MemoryStore) Create(ctx context.Context, record Record) (Record, bool, error) {
	if store == nil || ctx.Err() != nil {
		return Record{}, false, ErrStateUnavailable
	}
	record.Revision = 1
	if err := validateRecord(record); err != nil {
		return Record{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if current, exists := store.records[record.OperationID]; exists {
		return current, false, nil
	}
	store.records[record.OperationID] = record
	return record, true, nil
}

func (store *MemoryStore) Load(ctx context.Context, operationID string) (Record, bool, error) {
	if store == nil || ctx.Err() != nil || normalized(operationID) == "" {
		return Record{}, false, ErrStateUnavailable
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, exists := store.records[operationID]
	return record, exists, nil
}

func (store *MemoryStore) CompareAndSwap(ctx context.Context, operationID string, revision uint64, next Record) (Record, bool, error) {
	if store == nil || ctx.Err() != nil || normalized(operationID) == "" {
		return Record{}, false, ErrStateUnavailable
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.records[operationID]
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
	store.records[operationID] = next
	return next, true, nil
}

func (store *MemoryStore) PruneTerminalBefore(ctx context.Context, before time.Time) (int, error) {
	if store == nil || ctx.Err() != nil || before.IsZero() {
		return 0, ErrStateUnavailable
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	removed := 0
	for operationID, record := range store.records {
		if prunable(record) && record.UpdatedAt.Before(before) {
			delete(store.records, operationID)
			removed++
		}
	}
	return removed, nil
}
