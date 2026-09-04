package telegramflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"bria/internal/telegrambridge"
	"bria/internal/telegramops"
)

type CallbackAcknowledgementPhase string

const (
	CallbackAcknowledgementPending   CallbackAcknowledgementPhase = "pending"
	CallbackAcknowledgementConfirmed CallbackAcknowledgementPhase = "confirmed"
	CallbackAcknowledgementFailed    CallbackAcknowledgementPhase = "failed"
	CallbackAcknowledgementAbandoned CallbackAcknowledgementPhase = "abandoned"
)

type CallbackAcknowledgement struct {
	OperationID     string                       `json:"operation_id"`
	CallbackQueryID string                       `json:"callback_query_id"`
	Phase           CallbackAcknowledgementPhase `json:"phase"`
}

type CallbackAcknowledgementStore interface {
	CreateCallbackAcknowledgement(context.Context, string, string) error
	BeginCallbackAcknowledgement(context.Context, string, string) (bool, error)
	CompleteCallbackAcknowledgement(context.Context, string, string, telegrambridge.CallbackAcknowledgementState) error
	LoadCallbackAcknowledgement(context.Context, string) (CallbackAcknowledgement, bool, error)
}

func createCallbackAcknowledgement(ctx context.Context, backend telegramops.Store, mu *sync.Mutex, fresh map[string]bool, operationID, callbackQueryID string) error {
	acknowledgement := CallbackAcknowledgement{OperationID: operationID, CallbackQueryID: callbackQueryID, Phase: CallbackAcknowledgementPending}
	if err := validateCallbackAcknowledgement(acknowledgement); err != nil {
		return err
	}
	raw, _ := json.Marshal(acknowledgement)
	if err := backend.Insert(ctx, telegramops.Acknowledgements, operationID, raw); err != nil {
		if !errors.Is(err, telegramops.ErrExists) {
			return err
		}
		existing, found, loadErr := loadCallbackAcknowledgement(ctx, backend, operationID)
		if loadErr != nil || !found || existing.CallbackQueryID != callbackQueryID {
			return errors.New("callback acknowledgement identity collision")
		}
		return nil
	}
	mu.Lock()
	fresh[operationID] = true
	mu.Unlock()
	return nil
}

func beginCallbackAcknowledgement(ctx context.Context, backend telegramops.Store, mu *sync.Mutex, fresh map[string]bool, operationID, callbackQueryID string) (bool, error) {
	acknowledgement, found, err := loadCallbackAcknowledgement(ctx, backend, operationID)
	if err != nil || !found {
		return false, err
	}
	if acknowledgement.CallbackQueryID != callbackQueryID || acknowledgement.Phase != CallbackAcknowledgementPending {
		return false, nil
	}
	mu.Lock()
	currentProcess := fresh[operationID]
	delete(fresh, operationID)
	mu.Unlock()
	if currentProcess {
		return true, nil
	}
	abandoned := acknowledgement
	abandoned.Phase = CallbackAcknowledgementAbandoned
	raw, _ := json.Marshal(abandoned)
	_, err = backend.CompareAndSwap(ctx, telegramops.Acknowledgements, operationID, string(CallbackAcknowledgementPending), raw)
	return false, err
}

func completeCallbackAcknowledgement(ctx context.Context, backend telegramops.Store, operationID, callbackQueryID string, state telegrambridge.CallbackAcknowledgementState) error {
	acknowledgement, found, err := loadCallbackAcknowledgement(ctx, backend, operationID)
	if err != nil {
		return err
	}
	if !found || acknowledgement.CallbackQueryID != callbackQueryID || acknowledgement.Phase != CallbackAcknowledgementPending {
		return errors.New("callback acknowledgement is not pending")
	}
	switch state {
	case telegrambridge.CallbackAcknowledgementConfirmed:
		acknowledgement.Phase = CallbackAcknowledgementConfirmed
	case telegrambridge.CallbackAcknowledgementFailed:
		acknowledgement.Phase = CallbackAcknowledgementFailed
	default:
		return errors.New("callback acknowledgement outcome is invalid")
	}
	raw, _ := json.Marshal(acknowledgement)
	changed, err := backend.CompareAndSwap(ctx, telegramops.Acknowledgements, operationID, string(CallbackAcknowledgementPending), raw)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback acknowledgement changed before completion")
	}
	return nil
}

func loadCallbackAcknowledgement(ctx context.Context, backend telegramops.Store, operationID string) (CallbackAcknowledgement, bool, error) {
	if backend == nil || strings.TrimSpace(operationID) == "" {
		return CallbackAcknowledgement{}, false, errors.New("callback acknowledgement identity is required")
	}
	raw, found, err := backend.Load(ctx, telegramops.Acknowledgements, operationID)
	if err != nil || !found {
		return CallbackAcknowledgement{}, found, err
	}
	var acknowledgement CallbackAcknowledgement
	if err := decodeStrict(raw, &acknowledgement); err != nil {
		return CallbackAcknowledgement{}, false, err
	}
	if err := validateCallbackAcknowledgement(acknowledgement); err != nil {
		return CallbackAcknowledgement{}, false, err
	}
	return acknowledgement, true, nil
}

func validateCallbackAcknowledgement(acknowledgement CallbackAcknowledgement) error {
	if strings.TrimSpace(acknowledgement.OperationID) == "" || strings.TrimSpace(acknowledgement.CallbackQueryID) == "" {
		return errors.New("callback acknowledgement identity is required")
	}
	switch acknowledgement.Phase {
	case CallbackAcknowledgementPending, CallbackAcknowledgementConfirmed, CallbackAcknowledgementFailed, CallbackAcknowledgementAbandoned:
		return nil
	default:
		return errors.New("callback acknowledgement phase is invalid")
	}
}

func (store *FileCallbackOperationStore) CreateCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string) error {
	return createCallbackAcknowledgement(ctx, store.backend(), &store.ackMu, store.freshAcks, operationID, callbackQueryID)
}

func (store *MemoryCallbackOperationStore) CreateCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string) error {
	return createCallbackAcknowledgement(ctx, store.backend(), &store.ackMu, store.freshAcks, operationID, callbackQueryID)
}

func (store *FileCallbackOperationStore) BeginCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string) (bool, error) {
	return beginCallbackAcknowledgement(ctx, store.backend(), &store.ackMu, store.freshAcks, operationID, callbackQueryID)
}

func (store *MemoryCallbackOperationStore) BeginCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string) (bool, error) {
	return beginCallbackAcknowledgement(ctx, store.backend(), &store.ackMu, store.freshAcks, operationID, callbackQueryID)
}

func (store *FileCallbackOperationStore) CompleteCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string, state telegrambridge.CallbackAcknowledgementState) error {
	return completeCallbackAcknowledgement(ctx, store.backend(), operationID, callbackQueryID, state)
}

func (store *MemoryCallbackOperationStore) CompleteCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string, state telegrambridge.CallbackAcknowledgementState) error {
	return completeCallbackAcknowledgement(ctx, store.backend(), operationID, callbackQueryID, state)
}

func (store *FileCallbackOperationStore) LoadCallbackAcknowledgement(ctx context.Context, operationID string) (CallbackAcknowledgement, bool, error) {
	return loadCallbackAcknowledgement(ctx, store.backend(), operationID)
}

func (store *MemoryCallbackOperationStore) LoadCallbackAcknowledgement(ctx context.Context, operationID string) (CallbackAcknowledgement, bool, error) {
	return loadCallbackAcknowledgement(ctx, store.backend(), operationID)
}
