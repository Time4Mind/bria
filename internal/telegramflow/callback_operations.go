package telegramflow

import (
	"bria/internal/coordinator"
	"bria/internal/telegramops"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecovery/statusrecovery"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

const (
	callbackOperationStoreVersion = telegramops.Version
	maxCallbackOperationStoreSize = 16 << 20
	maxUnknownCallbackOperations  = 100
)

var ErrCallbackOperationExists = errors.New("callback operation already exists")

type CallbackOperationPhase string

const (
	CallbackClaimed            CallbackOperationPhase = "claimed"
	CallbackEffectUnknown      CallbackOperationPhase = "effect_unknown"
	CallbackEffectRetryUnknown CallbackOperationPhase = "effect_retry_unknown"
	CallbackEffectResolved     CallbackOperationPhase = "effect_resolved"
	CallbackPrepared           CallbackOperationPhase = "prepared"
	CallbackSendUnknown        CallbackOperationPhase = "send_unknown"
	CallbackReceiptConfirmed   CallbackOperationPhase = "receipt_confirmed"
	CallbackCommitted          CallbackOperationPhase = "committed"
)

type CallbackOperation struct {
	ID              string                        `json:"id"`
	UpdateID        int64                         `json:"update_id"`
	CallbackQueryID string                        `json:"callback_query_id"`
	CallbackDigest  string                        `json:"callback_digest"`
	Plan            telegrampipeline.CallbackPlan `json:"plan"`
	Phase           CallbackOperationPhase        `json:"phase"`
	Prepared        *Prepared                     `json:"prepared,omitempty"`
	Receipt         int64                         `json:"receipt,omitempty"`
}
type StatusOperationPhase string

const (
	StatusQueued           StatusOperationPhase = "queued"
	StatusSendUnknown      StatusOperationPhase = "send_unknown"
	StatusReceiptConfirmed StatusOperationPhase = "receipt_confirmed"
	StatusCommitted        StatusOperationPhase = "committed"
)

type StatusOperation struct {
	ID       string                      `json:"id"`
	Sequence uint64                      `json:"sequence"`
	Status   coordinator.Status          `json:"status"`
	Keyboard *coordinator.KeyboardMarkup `json:"keyboard,omitempty"`
	Prepared *Prepared                   `json:"prepared,omitempty"`
	Edit     bool                        `json:"edit"`
	Phase    StatusOperationPhase        `json:"phase"`
	Receipt  int64                       `json:"receipt,omitempty"`
	Recovery *StatusRecoveryBinding      `json:"recovery,omitempty"`
}
type RecoveryScopeKind = statusrecovery.ScopeKind
type RecoveryScope = statusrecovery.Scope
type StatusRecoveryBinding = statusrecovery.Binding

const (
	RecoveryScopeSession = statusrecovery.ScopeSession
	RecoveryScopeGlobal  = statusrecovery.ScopeGlobal
)

type CallbackOperationStore interface {
	Load(context.Context, string) (CallbackOperation, bool, error)
	ListUnknown(context.Context, int) ([]CallbackOperation, error)
	Create(context.Context, CallbackOperation) error
	CompareAndSwap(context.Context, string, CallbackOperationPhase, CallbackOperation) (bool, error)
	LoadStatus(context.Context, string) (StatusOperation, bool, error)
	EnqueueStatus(context.Context, StatusOperation) (StatusOperation, bool, error)
	ListQueuedStatuses(context.Context, int) ([]StatusOperation, error)
	ListUnknownStatuses(context.Context, int) ([]StatusOperation, error)
	CompareAndSwapStatus(context.Context, string, StatusOperationPhase, StatusOperation) (bool, error)
}
type FileCallbackOperationStore struct {
	raw           *telegramops.FileStore
	syncDirectory func(string) error
}

func OpenFileCallbackOperationStore(path string) (*FileCallbackOperationStore, error) {
	raw, err := telegramops.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("open callback operation store: %w", err)
	}
	store := &FileCallbackOperationStore{raw: raw}
	snapshot, err := raw.Snapshot(context.Background())
	if err != nil {
		return nil, err
	}
	if err := validateRawSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("validate callback operation store: %w", err)
	}
	return store, nil
}

type MemoryCallbackOperationStore struct{ raw telegramops.Store }

func NewMemoryCallbackOperationStore() *MemoryCallbackOperationStore {
	return &MemoryCallbackOperationStore{raw: telegramops.NewMemory()}
}
func (store *FileCallbackOperationStore) backend() telegramops.Store {
	if store == nil {
		return nil
	}
	if store.syncDirectory != nil {
		store.raw.SetDirectorySync(store.syncDirectory)
	}
	return store.raw
}
func (store *MemoryCallbackOperationStore) backend() telegramops.Store {
	if store == nil {
		return nil
	}
	return store.raw
}
func loadCallback(ctx context.Context, backend telegramops.Store, id string) (CallbackOperation, bool, error) {
	if backend == nil || id == "" {
		return CallbackOperation{}, false, errors.New("callback operation store and identity are required")
	}
	raw, found, err := backend.Load(ctx, telegramops.Callbacks, id)
	if err != nil || !found {
		return CallbackOperation{}, found, err
	}
	operation, err := decodeCallbackOperation(raw)
	return operation, err == nil, err
}
func (store *FileCallbackOperationStore) Load(ctx context.Context, id string) (CallbackOperation, bool, error) {
	return loadCallback(ctx, store.backend(), id)
}
func (store *MemoryCallbackOperationStore) Load(ctx context.Context, id string) (CallbackOperation, bool, error) {
	return loadCallback(ctx, store.backend(), id)
}
func createCallback(ctx context.Context, backend telegramops.Store, operation CallbackOperation) error {
	if backend == nil || operation.Phase != CallbackClaimed {
		return errors.New("new callback operation must be claimed")
	}
	if err := validateCallbackOperation(operation); err != nil {
		return err
	}
	raw, _ := json.Marshal(operation)
	err := backend.Insert(ctx, telegramops.Callbacks, operation.ID, raw)
	if errors.Is(err, telegramops.ErrExists) {
		return ErrCallbackOperationExists
	}
	return err
}
func (store *FileCallbackOperationStore) Create(ctx context.Context, operation CallbackOperation) error {
	return createCallback(ctx, store.backend(), operation)
}
func (store *MemoryCallbackOperationStore) Create(ctx context.Context, operation CallbackOperation) error {
	return createCallback(ctx, store.backend(), operation)
}
func swapCallback(ctx context.Context, backend telegramops.Store, id string, old CallbackOperationPhase, next CallbackOperation) (bool, error) {
	if backend == nil || next.ID != id || !validCallbackOperationTransition(old, next.Phase) {
		return false, errors.New("callback operation transition is invalid")
	}
	if err := validateCallbackOperation(next); err != nil {
		return false, err
	}
	current, found, err := loadCallback(ctx, backend, id)
	if err != nil || !found {
		return false, err
	}
	if !sameCallbackOperationIdentity(current, next) {
		return false, errors.New("callback operation immutable identity changed")
	}
	raw, _ := json.Marshal(next)
	return backend.CompareAndSwap(ctx, telegramops.Callbacks, id, string(old), raw)
}
func (store *FileCallbackOperationStore) CompareAndSwap(ctx context.Context, id string, old CallbackOperationPhase, next CallbackOperation) (bool, error) {
	return swapCallback(ctx, store.backend(), id, old, next)
}
func (store *MemoryCallbackOperationStore) CompareAndSwap(ctx context.Context, id string, old CallbackOperationPhase, next CallbackOperation) (bool, error) {
	return swapCallback(ctx, store.backend(), id, old, next)
}
func listCallbacks(ctx context.Context, backend telegramops.Store, limit int) ([]CallbackOperation, error) {
	if backend == nil || limit < 1 || limit > maxUnknownCallbackOperations {
		return nil, errors.New("callback unknown operation limit must be 1..100")
	}
	records, err := backend.List(ctx, telegramops.Callbacks, []string{string(CallbackEffectUnknown), string(CallbackEffectRetryUnknown), string(CallbackSendUnknown)}, limit)
	if err != nil {
		return nil, err
	}
	result := make([]CallbackOperation, len(records))
	for index, raw := range records {
		result[index], err = decodeCallbackOperation(raw)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (store *FileCallbackOperationStore) ListUnknown(ctx context.Context, limit int) ([]CallbackOperation, error) {
	return listCallbacks(ctx, store.backend(), limit)
}
func (store *MemoryCallbackOperationStore) ListUnknown(ctx context.Context, limit int) ([]CallbackOperation, error) {
	return listCallbacks(ctx, store.backend(), limit)
}
func loadStatus(ctx context.Context, backend telegramops.Store, id string) (StatusOperation, bool, error) {
	if backend == nil || id == "" {
		return StatusOperation{}, false, errors.New("status operation store and identity are required")
	}
	raw, found, err := backend.Load(ctx, telegramops.Statuses, id)
	if err != nil || !found {
		return StatusOperation{}, found, err
	}
	operation, err := decodeStatusOperation(raw)
	return operation, err == nil, err
}
func (store *FileCallbackOperationStore) LoadStatus(ctx context.Context, id string) (StatusOperation, bool, error) {
	return loadStatus(ctx, store.backend(), id)
}
func (store *MemoryCallbackOperationStore) LoadStatus(ctx context.Context, id string) (StatusOperation, bool, error) {
	return loadStatus(ctx, store.backend(), id)
}
func enqueueStatus(ctx context.Context, backend telegramops.Store, operation StatusOperation) (StatusOperation, bool, error) {
	if backend == nil || operation.Phase != StatusQueued {
		return StatusOperation{}, false, errors.New("new status operation must be queued")
	}
	if err := validateStatusOperation(operation); err != nil {
		return StatusOperation{}, false, err
	}
	if existing, found, err := loadStatus(ctx, backend, operation.ID); err != nil {
		return StatusOperation{}, false, err
	} else if found {
		if !sameStatusOperationIdentity(existing, operation) {
			return StatusOperation{}, false, errors.New("status operation identity collision")
		}
		return existing, false, nil
	}
	raw, _ := json.Marshal(operation)
	err := backend.Insert(ctx, telegramops.Statuses, operation.ID, raw)
	if errors.Is(err, telegramops.ErrExists) {
		existing, found, loadErr := loadStatus(ctx, backend, operation.ID)
		if loadErr != nil || !found || !sameStatusOperationIdentity(existing, operation) {
			return StatusOperation{}, false, errors.New("status operation identity collision")
		}
		return existing, false, nil
	}
	return cloneStatusOperation(operation), err == nil, err
}
func (store *FileCallbackOperationStore) EnqueueStatus(ctx context.Context, operation StatusOperation) (StatusOperation, bool, error) {
	return enqueueStatus(ctx, store.backend(), operation)
}
func (store *MemoryCallbackOperationStore) EnqueueStatus(ctx context.Context, operation StatusOperation) (StatusOperation, bool, error) {
	return enqueueStatus(ctx, store.backend(), operation)
}
func listStatuses(ctx context.Context, backend telegramops.Store, limit int, phase StatusOperationPhase) ([]StatusOperation, error) {
	if backend == nil || limit < 1 || limit > 100 {
		return nil, errors.New("status operation list limit must be 1..100")
	}
	records, err := backend.List(ctx, telegramops.Statuses, []string{string(phase)}, limit)
	if err != nil {
		return nil, err
	}
	result := make([]StatusOperation, len(records))
	for index, raw := range records {
		result[index], err = decodeStatusOperation(raw)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (store *FileCallbackOperationStore) ListQueuedStatuses(ctx context.Context, limit int) ([]StatusOperation, error) {
	return listStatuses(ctx, store.backend(), limit, StatusQueued)
}
func (store *MemoryCallbackOperationStore) ListQueuedStatuses(ctx context.Context, limit int) ([]StatusOperation, error) {
	return listStatuses(ctx, store.backend(), limit, StatusQueued)
}
func (store *FileCallbackOperationStore) ListUnknownStatuses(ctx context.Context, limit int) ([]StatusOperation, error) {
	return listStatuses(ctx, store.backend(), limit, StatusSendUnknown)
}
func (store *MemoryCallbackOperationStore) ListUnknownStatuses(ctx context.Context, limit int) ([]StatusOperation, error) {
	return listStatuses(ctx, store.backend(), limit, StatusSendUnknown)
}
func swapStatus(ctx context.Context, backend telegramops.Store, id string, old StatusOperationPhase, next StatusOperation) (bool, error) {
	if backend == nil || next.ID != id || !validStatusTransition(old, next.Phase) {
		return false, errors.New("status operation transition is invalid")
	}
	if err := validateStatusOperation(next); err != nil {
		return false, err
	}
	current, found, err := loadStatus(ctx, backend, id)
	if err != nil || !found {
		return false, err
	}
	if !sameStatusOperationIdentity(current, next) {
		return false, errors.New("status operation immutable identity changed")
	}
	raw, _ := json.Marshal(next)
	return backend.CompareAndSwap(ctx, telegramops.Statuses, id, string(old), raw)
}
func (store *FileCallbackOperationStore) CompareAndSwapStatus(ctx context.Context, id string, old StatusOperationPhase, next StatusOperation) (bool, error) {
	return swapStatus(ctx, store.backend(), id, old, next)
}
func (store *MemoryCallbackOperationStore) CompareAndSwapStatus(ctx context.Context, id string, old StatusOperationPhase, next StatusOperation) (bool, error) {
	return swapStatus(ctx, store.backend(), id, old, next)
}
func validCallbackOperationTransition(old, next CallbackOperationPhase) bool {
	switch old {
	case CallbackClaimed:
		return next == CallbackEffectUnknown
	case CallbackEffectUnknown:
		return next == CallbackPrepared || next == CallbackEffectResolved || next == CallbackEffectRetryUnknown
	case CallbackEffectRetryUnknown:
		return next == CallbackEffectResolved
	case CallbackPrepared:
		return next == CallbackPrepared || next == CallbackSendUnknown
	case CallbackSendUnknown:
		return next == CallbackPrepared || next == CallbackReceiptConfirmed
	case CallbackReceiptConfirmed:
		return next == CallbackCommitted
	}
	return false
}
func validStatusTransition(old, next StatusOperationPhase) bool {
	switch old {
	case StatusQueued:
		return next == StatusSendUnknown
	case StatusSendUnknown:
		return next == StatusQueued || next == StatusReceiptConfirmed
	case StatusReceiptConfirmed:
		return next == StatusCommitted
	}
	return false
}
func validateCallbackOperation(operation CallbackOperation) error {
	if strings.TrimSpace(operation.ID) == "" || operation.UpdateID <= 0 || operation.CallbackQueryID == "" {
		return errors.New("callback operation identity is required")
	}
	digest, err := hex.DecodeString(operation.CallbackDigest)
	if err != nil || len(digest) != 32 {
		return errors.New("callback operation digest must be SHA-256")
	}
	expected, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{UpdateID: operation.Plan.UpdateID, SessionID: operation.Plan.SessionID, Carrier: operation.Plan.Carrier, Action: operation.Plan.Action, Target: operation.Plan.Target, InteractionRequestID: interactionRequestID(operation.Plan), OutboundOperationID: outboundOperationID(operation.Plan), OutboundUpdateID: outboundUpdateID(operation.Plan), Recovery: recoveryBinding(operation.Plan), AcceptedTurnRecovery: acceptedTurnRecoveryBinding(operation.Plan), StatusRecovery: statusRecoveryBinding(operation.Plan), ArtifactRetry: artifactRetryBinding(operation.Plan)})
	if err == nil {
		expected.OperationID = operation.ID
	}
	if err != nil || !reflect.DeepEqual(expected, operation.Plan) {
		return errors.New("callback operation semantic plan is invalid")
	}
	if operation.Prepared != nil {
		validator := &pendingStore{items: make(map[string]Prepared)}
		if operation.Prepared.OperationID != operation.ID || validator.register(*operation.Prepared) != nil {
			return errors.New("callback operation prepared output is invalid")
		}
	}
	switch operation.Phase {
	case CallbackClaimed, CallbackEffectUnknown, CallbackEffectRetryUnknown, CallbackEffectResolved:
		if operation.Prepared != nil || operation.Receipt != 0 {
			return errors.New("unfinished callback operation contains output or receipt")
		}
	case CallbackPrepared, CallbackSendUnknown:
		if operation.Prepared == nil || operation.Receipt != 0 {
			return errors.New("prepared callback operation is invalid")
		}
	case CallbackReceiptConfirmed, CallbackCommitted:
		if operation.Prepared == nil || operation.Receipt <= 0 {
			return errors.New("confirmed callback operation is invalid")
		}
	default:
		return errors.New("unsupported callback operation phase")
	}
	return nil
}
func validateStatusOperation(operation StatusOperation) error {
	if strings.TrimSpace(operation.ID) == "" || operation.Sequence == 0 || operation.Status.ConversationID <= 0 || strings.TrimSpace(operation.Status.Text) == "" || operation.Edit != (operation.Status.SourceMessageID > 0) {
		return errors.New("status operation identity is invalid")
	}
	if operation.Prepared != nil {
		validator := &pendingStore{items: make(map[string]Prepared)}
		if operation.Prepared.OperationID != operation.ID || validator.register(*operation.Prepared) != nil || !reflect.DeepEqual(operation.Prepared.Status, operation.Status) || !reflect.DeepEqual(operation.Prepared.Keyboard, operation.Keyboard) || operation.Prepared.Edit != operation.Edit {
			return errors.New("status operation prepared output is invalid")
		}
	}
	if operation.Recovery != nil && !validStatusRecoveryBinding(*operation.Recovery, operation) {
		return errors.New("status recovery binding is invalid")
	}
	switch operation.Phase {
	case StatusQueued, StatusSendUnknown:
		if operation.Receipt != 0 {
			return errors.New("unconfirmed status contains receipt")
		}
	case StatusReceiptConfirmed, StatusCommitted:
		if operation.Receipt <= 0 {
			return errors.New("confirmed status requires receipt")
		}
	default:
		return errors.New("status operation phase is invalid")
	}
	return nil
}
func sameCallbackOperationIdentity(left, right CallbackOperation) bool {
	return left.ID == right.ID && left.UpdateID == right.UpdateID && left.CallbackQueryID == right.CallbackQueryID && left.CallbackDigest == right.CallbackDigest && reflect.DeepEqual(left.Plan, right.Plan)
}
func sameStatusOperationIdentity(left, right StatusOperation) bool {
	return left.ID == right.ID && left.Sequence == right.Sequence && reflect.DeepEqual(left.Status, right.Status) && reflect.DeepEqual(left.Keyboard, right.Keyboard) && reflect.DeepEqual(left.Prepared, right.Prepared) && left.Edit == right.Edit && reflect.DeepEqual(left.Recovery, right.Recovery)
}
func validStatusRecoveryBinding(binding StatusRecoveryBinding, operation StatusOperation) bool {
	if !statusrecovery.Valid(binding) || binding.OperationID != operation.ID || binding.Sequence != operation.Sequence || binding.Prepared != (operation.Prepared != nil) || binding.Edit != operation.Edit || binding.Carrier.ChatID != operation.Status.ConversationID {
		return false
	}
	return !operation.Edit || binding.Carrier.MessageID == operation.Status.SourceMessageID
}
func decodeCallbackOperation(raw json.RawMessage) (CallbackOperation, error) {
	var operation CallbackOperation
	if err := decodeStrict(raw, &operation); err != nil {
		return operation, err
	}
	if err := validateCallbackOperation(operation); err != nil {
		return operation, err
	}
	return cloneCallbackOperation(operation), nil
}
func decodeStatusOperation(raw json.RawMessage) (StatusOperation, error) {
	var operation StatusOperation
	if err := decodeStrict(raw, &operation); err != nil {
		return operation, err
	}
	if err := validateStatusOperation(operation); err != nil {
		return operation, err
	}
	return cloneStatusOperation(operation), nil
}
func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func validateRawSnapshot(snapshot telegramops.Snapshot) error {
	for _, raw := range snapshot.Operations {
		if _, err := decodeCallbackOperation(raw); err != nil {
			return err
		}
	}
	for _, raw := range snapshot.Statuses {
		if _, err := decodeStatusOperation(raw); err != nil {
			return err
		}
	}
	return nil
}
func cloneCallbackOperation(operation CallbackOperation) CallbackOperation {
	clone := operation
	clone.Plan = cloneCallbackPlan(operation.Plan)
	if clone.Prepared = clonePointer(operation.Prepared); clone.Prepared != nil {
		*clone.Prepared = clonePrepared(*clone.Prepared)
	}
	return clone
}
func cloneCallbackPlan(plan telegrampipeline.CallbackPlan) telegrampipeline.CallbackPlan {
	clone := plan
	clone.Interaction = clonePointer(plan.Interaction)
	clone.OutboundResolution = clonePointer(plan.OutboundResolution)
	clone.Recovery = clonePointer(plan.Recovery)
	clone.AcceptedTurnRecovery = clonePointer(plan.AcceptedTurnRecovery)
	clone.StatusRecovery = clonePointer(plan.StatusRecovery)
	clone.ArtifactRetry = clonePointer(plan.ArtifactRetry)
	return clone
}
func cloneStatusOperation(operation StatusOperation) StatusOperation {
	clone := operation
	clone.Keyboard = cloneCoordinatorKeyboard(operation.Keyboard)
	if clone.Prepared = clonePointer(operation.Prepared); clone.Prepared != nil {
		*clone.Prepared = clonePrepared(*clone.Prepared)
	}
	clone.Recovery = clonePointer(operation.Recovery)
	return clone
}
func interactionRequestID(plan telegrampipeline.CallbackPlan) string {
	if plan.Interaction == nil {
		return ""
	}
	return plan.Interaction.RequestID
}
func outboundOperationID(plan telegrampipeline.CallbackPlan) string {
	if plan.OutboundResolution == nil {
		return ""
	}
	return plan.OutboundResolution.OperationID
}
func outboundUpdateID(plan telegrampipeline.CallbackPlan) int64 {
	if plan.OutboundResolution == nil {
		return 0
	}
	return plan.OutboundResolution.UpdateID
}
func recoveryBinding(plan telegrampipeline.CallbackPlan) *telegrampipeline.CallbackRecoveryBinding {
	if plan.Recovery == nil {
		return nil
	}
	return &telegrampipeline.CallbackRecoveryBinding{OperationID: plan.Recovery.OperationID, UpdateID: plan.Recovery.UpdateID, SessionID: plan.Recovery.SessionID, Carrier: plan.Recovery.Carrier, Phase: plan.Recovery.Phase}
}
func acceptedTurnRecoveryBinding(plan telegrampipeline.CallbackPlan) *telegrampipeline.AcceptedTurnRecoveryBinding {
	if plan.AcceptedTurnRecovery == nil {
		return nil
	}
	return &telegrampipeline.AcceptedTurnRecoveryBinding{
		SessionID: plan.AcceptedTurnRecovery.SessionID, MessageID: plan.AcceptedTurnRecovery.MessageID,
		BindingGeneration: plan.AcceptedTurnRecovery.BindingGeneration,
	}
}
func statusRecoveryBinding(plan telegrampipeline.CallbackPlan) *telegrampipeline.StatusRecoveryBinding {
	if plan.StatusRecovery == nil {
		return nil
	}
	value := plan.StatusRecovery.Binding
	return &value
}
func artifactRetryBinding(plan telegrampipeline.CallbackPlan) *telegrampipeline.ArtifactRetryBinding {
	if plan.ArtifactRetry == nil {
		return nil
	}
	value := plan.ArtifactRetry.Binding
	return &value
}
