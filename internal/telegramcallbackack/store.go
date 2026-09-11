// Package telegramcallbackack persists one-shot Telegram callback acknowledgements.
package telegramcallbackack

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"bria/internal/telegrambridge"
	"bria/internal/telegramops"
)

type Phase string

const (
	Pending   Phase = "pending"
	Confirmed Phase = "confirmed"
	Failed    Phase = "failed"
	Abandoned Phase = "abandoned"
)

type Acknowledgement struct {
	OperationID     string `json:"operation_id"`
	CallbackQueryID string `json:"callback_query_id"`
	Phase           Phase  `json:"phase"`
}

type Tracker struct {
	mu    sync.Mutex
	fresh map[string]bool
}

func NewTracker() *Tracker { return &Tracker{fresh: make(map[string]bool)} }

type Recorder interface {
	CreateCallbackAcknowledgement(context.Context, string, string) error
	BeginCallbackAcknowledgement(context.Context, string, string) (bool, error)
	CompleteCallbackAcknowledgement(context.Context, string, string, telegrambridge.CallbackAcknowledgementState) error
	LoadCallbackAcknowledgement(context.Context, string) (Acknowledgement, bool, error)
}

type Adapter struct {
	tracker *Tracker
	backend func() telegramops.Store
}

func NewAdapter(backend func() telegramops.Store) *Adapter {
	return &Adapter{tracker: NewTracker(), backend: backend}
}

func (adapter *Adapter) CreateCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string) error {
	return adapter.tracker.Create(ctx, adapter.backend(), operationID, callbackQueryID)
}
func (adapter *Adapter) BeginCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string) (bool, error) {
	return adapter.tracker.Begin(ctx, adapter.backend(), operationID, callbackQueryID)
}
func (adapter *Adapter) CompleteCallbackAcknowledgement(ctx context.Context, operationID, callbackQueryID string, state telegrambridge.CallbackAcknowledgementState) error {
	return adapter.tracker.Complete(ctx, adapter.backend(), operationID, callbackQueryID, state)
}
func (adapter *Adapter) LoadCallbackAcknowledgement(ctx context.Context, operationID string) (Acknowledgement, bool, error) {
	return Load(ctx, adapter.backend(), operationID)
}

func (tracker *Tracker) Create(ctx context.Context, backend telegramops.Store, operationID, callbackQueryID string) error {
	ack := Acknowledgement{OperationID: operationID, CallbackQueryID: callbackQueryID, Phase: Pending}
	if err := Validate(ack); err != nil {
		return err
	}
	raw, _ := json.Marshal(ack)
	if err := backend.Insert(ctx, telegramops.Acknowledgements, operationID, raw); err != nil {
		if !errors.Is(err, telegramops.ErrExists) {
			return err
		}
		existing, found, loadErr := Load(ctx, backend, operationID)
		if loadErr != nil || !found || existing.CallbackQueryID != callbackQueryID {
			return errors.New("callback acknowledgement identity collision")
		}
		return nil
	}
	tracker.mu.Lock()
	tracker.fresh[operationID] = true
	tracker.mu.Unlock()
	return nil
}

func (tracker *Tracker) Begin(ctx context.Context, backend telegramops.Store, operationID, callbackQueryID string) (bool, error) {
	ack, found, err := Load(ctx, backend, operationID)
	if err != nil || !found {
		return false, err
	}
	if ack.CallbackQueryID != callbackQueryID || ack.Phase != Pending {
		return false, nil
	}
	tracker.mu.Lock()
	currentProcess := tracker.fresh[operationID]
	delete(tracker.fresh, operationID)
	tracker.mu.Unlock()
	if currentProcess {
		return true, nil
	}
	ack.Phase = Abandoned
	raw, _ := json.Marshal(ack)
	_, err = backend.CompareAndSwap(ctx, telegramops.Acknowledgements, operationID, string(Pending), raw)
	return false, err
}

func (tracker *Tracker) Complete(ctx context.Context, backend telegramops.Store, operationID, callbackQueryID string, state telegrambridge.CallbackAcknowledgementState) error {
	ack, found, err := Load(ctx, backend, operationID)
	if err != nil {
		return err
	}
	if !found || ack.CallbackQueryID != callbackQueryID || ack.Phase != Pending {
		return errors.New("callback acknowledgement is not pending")
	}
	switch state {
	case telegrambridge.CallbackAcknowledgementConfirmed:
		ack.Phase = Confirmed
	case telegrambridge.CallbackAcknowledgementFailed:
		ack.Phase = Failed
	default:
		return errors.New("callback acknowledgement outcome is invalid")
	}
	raw, _ := json.Marshal(ack)
	changed, err := backend.CompareAndSwap(ctx, telegramops.Acknowledgements, operationID, string(Pending), raw)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback acknowledgement changed before completion")
	}
	return nil
}

func Load(ctx context.Context, backend telegramops.Store, operationID string) (Acknowledgement, bool, error) {
	if backend == nil || strings.TrimSpace(operationID) == "" {
		return Acknowledgement{}, false, errors.New("callback acknowledgement identity is required")
	}
	raw, found, err := backend.Load(ctx, telegramops.Acknowledgements, operationID)
	if err != nil || !found {
		return Acknowledgement{}, found, err
	}
	ack, err := Decode(raw)
	return ack, err == nil, err
}

func Decode(raw json.RawMessage) (Acknowledgement, error) {
	var ack Acknowledgement
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ack); err != nil {
		return Acknowledgement{}, err
	}
	return ack, Validate(ack)
}

func Validate(ack Acknowledgement) error {
	if strings.TrimSpace(ack.OperationID) == "" || strings.TrimSpace(ack.CallbackQueryID) == "" {
		return errors.New("callback acknowledgement identity is required")
	}
	switch ack.Phase {
	case Pending, Confirmed, Failed, Abandoned:
		return nil
	default:
		return errors.New("callback acknowledgement phase is invalid")
	}
}
