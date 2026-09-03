package telegramflow

import (
	"context"
	"encoding/json"
	"errors"
)

type CallbackStateSnapshot struct {
	Version    int                          `json:"version"`
	Operations map[string]CallbackOperation `json:"operations"`
	Statuses   map[string]StatusOperation   `json:"statuses"`
}

const CallbackStateSnapshotVersion = callbackOperationStoreVersion

func (snapshot CallbackStateSnapshot) Validate() error {
	if snapshot.Operations == nil || snapshot.Statuses == nil {
		return errors.New("callback operation snapshot maps are required")
	}
	if snapshot.Version != CallbackStateSnapshotVersion {
		return errors.New("callback operation snapshot version is invalid")
	}
	for id, operation := range snapshot.Operations {
		if id != operation.ID {
			return errors.New("callback operation snapshot identity is invalid")
		}
		if err := validateCallbackOperation(operation); err != nil {
			return err
		}
	}
	for id, operation := range snapshot.Statuses {
		if id != operation.ID {
			return errors.New("status operation snapshot identity is invalid")
		}
		if err := validateStatusOperation(operation); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || len(encoded) > maxCallbackOperationStoreSize {
		return errors.New("callback operation snapshot exceeds size limit")
	}
	return nil
}
func (store *FileCallbackOperationStore) Snapshot(ctx context.Context) (CallbackStateSnapshot, error) {
	if ctx == nil {
		return CallbackStateSnapshot{}, errors.New("callback snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return CallbackStateSnapshot{}, err
	}
	if store == nil {
		return CallbackStateSnapshot{}, errors.New("callback operation store is required")
	}
	raw, err := store.raw.ReopenSnapshot(ctx)
	if err != nil {
		return CallbackStateSnapshot{}, err
	}
	snapshot := CallbackStateSnapshot{
		Version: raw.Version, Operations: make(map[string]CallbackOperation, len(raw.Operations)),
		Statuses: make(map[string]StatusOperation, len(raw.Statuses)),
	}
	for id, record := range raw.Operations {
		operation, decodeErr := decodeCallbackOperation(record)
		if decodeErr != nil {
			return CallbackStateSnapshot{}, decodeErr
		}
		snapshot.Operations[id] = operation
	}
	for id, record := range raw.Statuses {
		operation, decodeErr := decodeStatusOperation(record)
		if decodeErr != nil {
			return CallbackStateSnapshot{}, decodeErr
		}
		snapshot.Statuses[id] = operation
	}
	if err := snapshot.Validate(); err != nil {
		return CallbackStateSnapshot{}, err
	}
	return snapshot, nil
}
