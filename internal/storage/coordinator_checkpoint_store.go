package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"bria/internal/coordinator"
)

// CoordinatorCheckpointStore persists the coordinator checkpoint in the same
// atomic state document as sessions. Stores opened for the same path share the
// same process-local serialization lock.
type CoordinatorCheckpointStore struct {
	state *SessionStore
}

var _ coordinator.CheckpointStore = (*CoordinatorCheckpointStore)(nil)

// OpenCoordinatorCheckpointStore opens the unified state document at path.
// A missing document is an empty checkpoint store and is not created by Load.
func OpenCoordinatorCheckpointStore(path string) (*CoordinatorCheckpointStore, error) {
	state, err := OpenSessionStore(path)
	if err != nil {
		return nil, err
	}
	return state.CoordinatorCheckpoints(), nil
}

// CoordinatorCheckpoints returns a checkpoint view over this session store's
// unified state document.
func (store *SessionStore) CoordinatorCheckpoints() *CoordinatorCheckpointStore {
	return &CoordinatorCheckpointStore{state: store}
}

// Load rereads the durable state document before returning its checkpoint.
func (store *CoordinatorCheckpointStore) Load(
	ctx context.Context,
) (coordinator.StoredCheckpoint, bool, error) {
	if err := ctx.Err(); err != nil {
		return coordinator.StoredCheckpoint{}, false, err
	}
	store.state.mu.Lock()
	defer store.state.mu.Unlock()
	if err := store.state.reload(); err != nil {
		return coordinator.StoredCheckpoint{}, false, err
	}
	if store.state.checkpoint == nil {
		return coordinator.StoredCheckpoint{}, false, nil
	}
	stored, err := storedCheckpointFromRecord(store.state.checkpoint)
	if err != nil {
		return coordinator.StoredCheckpoint{}, false, fmt.Errorf("restore coordinator checkpoint: %w", err)
	}
	return stored, true, nil
}

// Save atomically replaces the checkpoint at expectedRevision. Replaying the
// immediately preceding successful write with the same value is idempotent.
// The committed document is reread and compared before Save reports success.
func (store *CoordinatorCheckpointStore) Save(
	ctx context.Context,
	expectedRevision uint64,
	next coordinator.Checkpoint,
) (coordinator.StoredCheckpoint, error) {
	if err := ctx.Err(); err != nil {
		return coordinator.StoredCheckpoint{}, err
	}
	if err := validateCheckpoint(next); err != nil {
		return coordinator.StoredCheckpoint{}, fmt.Errorf("validate coordinator checkpoint: %w", err)
	}
	if expectedRevision == math.MaxUint64 {
		return coordinator.StoredCheckpoint{}, errors.New("coordinator checkpoint revision overflow")
	}

	store.state.mu.Lock()
	defer store.state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return coordinator.StoredCheckpoint{}, err
	}
	if err := store.state.reload(); err != nil {
		return coordinator.StoredCheckpoint{}, err
	}

	if store.state.checkpoint == nil {
		if expectedRevision != 0 {
			return coordinator.StoredCheckpoint{}, ErrCompareAndSwapConflict
		}
	} else if store.state.checkpoint.Revision != expectedRevision {
		current, err := storedCheckpointFromRecord(store.state.checkpoint)
		if err != nil {
			return coordinator.StoredCheckpoint{}, fmt.Errorf("restore current coordinator checkpoint: %w", err)
		}
		if current.Revision == expectedRevision+1 && reflect.DeepEqual(current.Checkpoint, next) {
			return current, nil
		}
		return coordinator.StoredCheckpoint{}, ErrCompareAndSwapConflict
	}

	want := coordinator.StoredCheckpoint{
		Revision:   expectedRevision + 1,
		Checkpoint: cloneCheckpoint(next),
	}
	nextRecord := recordFromStoredCheckpoint(want)
	if err := writeSessionFile(store.state.path, store.state.byIntent, nextRecord, store.state.telegramUI); err != nil {
		if reloadErr := store.state.reload(); reloadErr != nil {
			return coordinator.StoredCheckpoint{}, errors.Join(
				fmt.Errorf("persist coordinator checkpoint: %w", err),
				reloadErr,
			)
		}
		return coordinator.StoredCheckpoint{}, fmt.Errorf("persist coordinator checkpoint: %w", err)
	}

	byIntent, byID, persistedRecord, persistedUI, err := readSessionFile(store.state.path)
	if err != nil {
		return coordinator.StoredCheckpoint{}, fmt.Errorf("reread coordinator checkpoint: %w", err)
	}
	if persistedRecord == nil {
		return coordinator.StoredCheckpoint{}, errors.New("reread coordinator checkpoint: checkpoint is absent")
	}
	persisted, err := storedCheckpointFromRecord(persistedRecord)
	if err != nil {
		return coordinator.StoredCheckpoint{}, fmt.Errorf("reread coordinator checkpoint: %w", err)
	}
	if !reflect.DeepEqual(persisted, want) {
		return coordinator.StoredCheckpoint{}, errors.New("reread coordinator checkpoint: persisted value mismatch")
	}
	store.state.byIntent = byIntent
	store.state.byID = byID
	store.state.checkpoint = persistedRecord
	store.state.telegramUI = persistedUI
	return persisted, nil
}

func validateCheckpoint(checkpoint coordinator.Checkpoint) error {
	if !checkpoint.Initialized {
		return errors.New("checkpoint must be initialized")
	}
	if checkpoint.NextUpdateID < 0 {
		return errors.New("next update id must not be negative")
	}
	if checkpoint.Blocked != nil {
		if checkpoint.Blocked.UpdateID <= 0 || checkpoint.Blocked.UpdateID < checkpoint.NextUpdateID {
			return errors.New("blocked update id must not precede next update id")
		}
		if strings.TrimSpace(checkpoint.Blocked.Reason) == "" {
			return errors.New("blocked update reason is required")
		}
	}
	if checkpoint.Outbound == nil {
		return nil
	}
	outbound := checkpoint.Outbound
	if strings.TrimSpace(outbound.OperationID) == "" {
		return errors.New("outbound operation id is required")
	}
	if outbound.UpdateID <= 0 {
		return errors.New("outbound update id must be positive")
	}
	if outbound.Status.ConversationID <= 0 {
		return errors.New("outbound conversation id must be positive")
	}
	if strings.TrimSpace(outbound.Status.Text) == "" {
		return errors.New("outbound status text is required")
	}
	switch outbound.Phase {
	case coordinator.OutboundPrepared, coordinator.OutboundUnknown:
		if outbound.Receipt != nil || outbound.Durable != nil || outbound.UpdateID < checkpoint.NextUpdateID {
			return fmt.Errorf("outbound phase %q has invalid receipt or offset", outbound.Phase)
		}
	case coordinator.OutboundEnqueued:
		if outbound.Receipt != nil || outbound.Durable == nil || outbound.Durable.OperationID != outbound.OperationID ||
			outbound.Durable.Sequence == 0 || outbound.UpdateID == math.MaxInt64 || checkpoint.NextUpdateID <= outbound.UpdateID {
			return errors.New("enqueued outbound requires an exact durable receipt and advanced offset")
		}
	case coordinator.OutboundConfirmed:
		if outbound.Receipt == nil || outbound.Durable != nil || outbound.Receipt.MessageID <= 0 ||
			outbound.UpdateID == math.MaxInt64 || checkpoint.NextUpdateID <= outbound.UpdateID {
			return errors.New("confirmed outbound requires a positive receipt and advanced offset")
		}
	default:
		return fmt.Errorf("unsupported outbound phase %q", outbound.Phase)
	}
	return nil
}

func recordFromStoredCheckpoint(stored coordinator.StoredCheckpoint) *coordinatorRecord {
	record := &coordinatorRecord{
		Version:      coordinatorCheckpointFormatVersion,
		Revision:     stored.Revision,
		Initialized:  stored.Checkpoint.Initialized,
		NextUpdateID: stored.Checkpoint.NextUpdateID,
	}
	if stored.Checkpoint.Blocked != nil {
		record.Blocked = &blockedUpdateRecord{
			UpdateID: stored.Checkpoint.Blocked.UpdateID,
			Reason:   stored.Checkpoint.Blocked.Reason,
		}
	}
	if stored.Checkpoint.Outbound != nil {
		outbound := stored.Checkpoint.Outbound
		record.Outbound = &outboundOperationRecord{
			OperationID: outbound.OperationID,
			UpdateID:    outbound.UpdateID,
			Status: statusRecord{
				ConversationID:  outbound.Status.ConversationID,
				Text:            outbound.Status.Text,
				CallbackQueryID: outbound.Status.CallbackQueryID,
				SourceMessageID: outbound.Status.SourceMessageID,
			},
			Phase: string(outbound.Phase),
		}
		if outbound.Receipt != nil {
			record.Outbound.Receipt = &receiptRecord{MessageID: outbound.Receipt.MessageID}
		}
		if outbound.Durable != nil {
			record.Outbound.Durable = &durableOutboundReceiptRecord{OperationID: outbound.Durable.OperationID, Sequence: outbound.Durable.Sequence}
		}
		if outbound.Keyboard != nil {
			record.Outbound.Keyboard = make([][]keyboardButtonRecord, len(*outbound.Keyboard))
			for row := range *outbound.Keyboard {
				record.Outbound.Keyboard[row] = make([]keyboardButtonRecord, len((*outbound.Keyboard)[row]))
				for column, button := range (*outbound.Keyboard)[row] {
					record.Outbound.Keyboard[row][column] = keyboardButtonRecord{Text: button.Text, CallbackData: button.CallbackData}
				}
			}
		}
	}
	return record
}

func storedCheckpointFromRecord(record *coordinatorRecord) (coordinator.StoredCheckpoint, error) {
	checkpoint := coordinator.Checkpoint{
		Initialized:  record.Initialized,
		NextUpdateID: record.NextUpdateID,
	}
	if record.Blocked != nil {
		checkpoint.Blocked = &coordinator.BlockedUpdate{
			UpdateID: record.Blocked.UpdateID,
			Reason:   record.Blocked.Reason,
		}
	}
	if record.Outbound != nil {
		checkpoint.Outbound = &coordinator.Outbound{
			OperationID: record.Outbound.OperationID,
			UpdateID:    record.Outbound.UpdateID,
			Status: coordinator.Status{
				ConversationID:  record.Outbound.Status.ConversationID,
				Text:            record.Outbound.Status.Text,
				CallbackQueryID: record.Outbound.Status.CallbackQueryID,
				SourceMessageID: record.Outbound.Status.SourceMessageID,
			},
			Phase: coordinator.OutboundPhase(record.Outbound.Phase),
		}
		if record.Outbound.Receipt != nil {
			checkpoint.Outbound.Receipt = &coordinator.Receipt{MessageID: record.Outbound.Receipt.MessageID}
		}
		if record.Outbound.Durable != nil {
			checkpoint.Outbound.Durable = &coordinator.DurableOutboundReceipt{OperationID: record.Outbound.Durable.OperationID, Sequence: record.Outbound.Durable.Sequence}
		}
		if record.Outbound.Keyboard != nil {
			keyboard := make(coordinator.KeyboardMarkup, len(record.Outbound.Keyboard))
			for row := range record.Outbound.Keyboard {
				keyboard[row] = make([]coordinator.KeyboardButton, len(record.Outbound.Keyboard[row]))
				for column, button := range record.Outbound.Keyboard[row] {
					keyboard[row][column] = coordinator.KeyboardButton{Text: button.Text, CallbackData: button.CallbackData}
				}
			}
			checkpoint.Outbound.Keyboard = &keyboard
		}
	}
	if err := validateCheckpoint(checkpoint); err != nil {
		return coordinator.StoredCheckpoint{}, err
	}
	if record.Revision == 0 {
		return coordinator.StoredCheckpoint{}, errors.New("persisted checkpoint revision must be positive")
	}
	return coordinator.StoredCheckpoint{
		Revision:   record.Revision,
		Checkpoint: checkpoint,
	}, nil
}

func cloneCheckpoint(checkpoint coordinator.Checkpoint) coordinator.Checkpoint {
	if checkpoint.Blocked != nil {
		blocked := *checkpoint.Blocked
		checkpoint.Blocked = &blocked
	}
	if checkpoint.Outbound != nil {
		outbound := *checkpoint.Outbound
		if outbound.Keyboard != nil {
			keyboard := make(coordinator.KeyboardMarkup, len(*outbound.Keyboard))
			for row := range *outbound.Keyboard {
				keyboard[row] = append([]coordinator.KeyboardButton(nil), (*outbound.Keyboard)[row]...)
			}
			outbound.Keyboard = &keyboard
		}
		if outbound.Receipt != nil {
			receipt := *outbound.Receipt
			outbound.Receipt = &receipt
		}
		if outbound.Durable != nil {
			durable := *outbound.Durable
			outbound.Durable = &durable
		}
		checkpoint.Outbound = &outbound
	}
	return checkpoint
}
