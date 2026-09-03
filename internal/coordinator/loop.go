// Package coordinator owns the single durable sequence in which external
// updates are admitted, handled, and acknowledged.
package coordinator

import (
	"bria/internal/coordinator/recoverycontrol"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrDeliveryUnknown       = errors.New("outbound delivery is unknown")
	ErrUpdateBlocked         = errors.New("update processing is blocked")
	ErrInvalidUpdateSequence = errors.New("invalid update sequence")
	ErrCheckpointMismatch    = errors.New("persisted checkpoint does not match reread state")
	ErrInvalidCheckpoint     = errors.New("invalid coordinator checkpoint")
)

type Update struct {
	ID                   int64
	Kind                 UpdateKind
	ActorID              int64
	ConversationID       int64
	ConversationKind     string
	Text                 string
	Caption              string
	CallbackQueryID      string
	SourceMessageID      int64
	ReplyToMessageID     int64
	MediaKind            string
	MediaFileID          string
	MediaFileUniqueID    string
	MediaFileSize        int64
	MediaMIMEType        string
	MediaDurationSeconds int
	MediaWidth           int
	MediaHeight          int
	MediaDownloadAllowed bool
}
type UpdateKind string

const (
	// UpdateUnsupported represents a transport update which was safely decoded
	// only far enough to preserve its ID. Every other payload field must be zero.
	UpdateUnsupported UpdateKind = ""
	UpdateMessage     UpdateKind = "message"
	UpdateCallback    UpdateKind = "callback"
)

type DecisionKind string

const (
	DecisionSkip   DecisionKind = "skip"
	DecisionStatus DecisionKind = "status"
	DecisionBlock  DecisionKind = "block"
)

type Decision struct {
	Kind        DecisionKind
	Status      Status
	BlockReason string
	// Keyboard is an optional transport-neutral inline keyboard. Senders that
	// support it render it; legacy senders retain the text-only behavior.
	Keyboard *KeyboardMarkup
}
type KeyboardButton struct{ Text, CallbackData string }
type KeyboardMarkup [][]KeyboardButton
type Status struct {
	ConversationID  int64
	Text            string
	CallbackQueryID string
	SourceMessageID int64
}
type Receipt struct {
	MessageID int64
}
type OutboundPhase string

const (
	OutboundPrepared  OutboundPhase = "prepared"
	OutboundUnknown   OutboundPhase = "unknown"
	OutboundEnqueued  OutboundPhase = "enqueued"
	OutboundConfirmed OutboundPhase = "confirmed"
)

type Outbound struct {
	OperationID string
	UpdateID    int64
	Status      Status
	Keyboard    *KeyboardMarkup
	Phase       OutboundPhase
	Receipt     *Receipt
	Durable     *DurableOutboundReceipt
}
type BlockedUpdate struct {
	UpdateID int64
	Reason   string
}
type Checkpoint struct {
	Initialized  bool
	NextUpdateID int64
	Blocked      *BlockedUpdate
	Outbound     *Outbound
	Recovery     *RecoveryControl
}
type StoredCheckpoint struct {
	Revision   uint64
	Checkpoint Checkpoint
}
type Source interface {
	Bootstrap(context.Context) (nextUpdateID int64, err error)
	Poll(context.Context, int64) ([]Update, error)
}
type CheckpointStore interface {
	Load(context.Context) (StoredCheckpoint, bool, error)
	Save(context.Context, uint64, Checkpoint) (StoredCheckpoint, error)
}
type Handler interface {
	Handle(context.Context, Update) (Decision, error)
}
type Sender interface {
	SendStatus(context.Context, string, Status) (Receipt, error)
}
type KeyboardSender interface {
	SendStatusWithKeyboard(context.Context, string, Status, *KeyboardMarkup) (Receipt, error)
}
type DurableOutboundReceipt struct {
	OperationID string
	Sequence    uint64
}
type DurableStatusSender interface {
	EnqueueStatus(context.Context, string, Status, *KeyboardMarkup) (DurableOutboundReceipt, error)
}
type CarrierEditor interface {
	EditStatusWithKeyboard(context.Context, string, Status, *KeyboardMarkup) (Receipt, error)
}
type OutboundReceiptResolver interface {
	ResolveStatusReceipt(context.Context, string) (Receipt, bool, error)
}
type UnknownOutbound struct {
	OperationID string
	UpdateID    int64
	Status      Status
}
type Readiness interface {
	Ready(context.Context, Checkpoint) error
}
type Loop struct {
	source    Source
	store     CheckpointStore
	handler   Handler
	sender    Sender
	readiness Readiness
	mutation  sync.Mutex
}

func NewLoop(
	source Source,
	store CheckpointStore,
	handler Handler,
	sender Sender,
	readiness Readiness,
) (*Loop, error) {
	if source == nil || store == nil || handler == nil || sender == nil || readiness == nil {
		return nil, errors.New("coordinator source, store, handler, sender, and readiness are required")
	}
	return &Loop{
		source:    source,
		store:     store,
		handler:   handler,
		sender:    sender,
		readiness: readiness,
	}, nil
}
func (loop *Loop) Run(ctx context.Context) error {
	stored, found, err := loop.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("load coordinator checkpoint: %w", err)
	}
	if found {
		if err := validateStored(stored); err != nil {
			return err
		}
	}
	// Readiness is the authenticated identity gate. It must run before the
	// first Bootstrap call because Bootstrap may destructively forget another
	// bot's pending updates when the configured token belongs to the wrong bot.
	readinessCheckpoint := Checkpoint{}
	if found {
		readinessCheckpoint = cloneCheckpoint(stored.Checkpoint)
	}
	if err := loop.readiness.Ready(ctx, readinessCheckpoint); err != nil {
		return fmt.Errorf("verify coordinator readiness: %w", err)
	}
	if !found {
		stored, err = loop.bootstrap(ctx)
		if err != nil {
			return err
		}
	}
	stored, err = loop.resolveInterruptedState(ctx, stored)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		updates, err := loop.source.Poll(ctx, stored.Checkpoint.NextUpdateID)
		if err != nil {
			return err
		}
		if err := validateUpdates(stored.Checkpoint.NextUpdateID, updates); err != nil {
			return err
		}
		for _, update := range updates {
			stored, err = loop.handleUpdate(ctx, stored, update)
			if err != nil {
				return err
			}
		}
	}
}
func (loop *Loop) bootstrap(ctx context.Context) (StoredCheckpoint, error) {
	nextUpdateID, err := loop.source.Bootstrap(ctx)
	if err != nil {
		return StoredCheckpoint{}, fmt.Errorf("bootstrap update source: %w", err)
	}
	next := Checkpoint{Initialized: true, NextUpdateID: nextUpdateID}
	if err := validateCheckpoint(next); err != nil {
		return StoredCheckpoint{}, err
	}
	return loop.saveVerified(ctx, StoredCheckpoint{}, next)
}
func (loop *Loop) resolveInterruptedState(
	ctx context.Context,
	stored StoredCheckpoint,
) (StoredCheckpoint, error) {
	loop.mutation.Lock()
	defer loop.mutation.Unlock()
	latest, found, err := loop.store.Load(ctx)
	if err != nil {
		return StoredCheckpoint{}, fmt.Errorf("refresh coordinator checkpoint before recovery: %w", err)
	}
	if !found {
		return StoredCheckpoint{}, ErrCheckpointMismatch
	}
	if err := validateStored(latest); err != nil {
		return StoredCheckpoint{}, err
	}
	stored = latest
	checkpoint := stored.Checkpoint
	if checkpoint.Blocked != nil {
		return StoredCheckpoint{}, fmt.Errorf("%w at update %d", ErrUpdateBlocked, checkpoint.Blocked.UpdateID)
	}
	if checkpoint.Outbound == nil || checkpoint.Outbound.Phase == OutboundConfirmed {
		return stored, nil
	}
	if checkpoint.Outbound.Phase == OutboundEnqueued {
		if receipt, confirmed, resolveErr := loop.resolveOutboundReceipt(ctx, checkpoint.Outbound.OperationID); resolveErr != nil {
			return StoredCheckpoint{}, resolveErr
		} else if confirmed {
			return loop.confirmOutbound(ctx, stored, receipt)
		}
		// Inner delivery is independently recoverable and must not block polling.
		return stored, nil
	}
	if receipt, confirmed, resolveErr := loop.resolveOutboundReceipt(ctx, checkpoint.Outbound.OperationID); resolveErr != nil {
		return StoredCheckpoint{}, resolveErr
	} else if confirmed {
		return loop.confirmOutbound(ctx, stored, receipt)
	}
	if checkpoint.Outbound.Phase == OutboundUnknown {
		return StoredCheckpoint{}, fmt.Errorf(
			"%w for operation %q",
			ErrDeliveryUnknown,
			checkpoint.Outbound.OperationID,
		)
	}
	// A crash can happen after the external request but before its receipt is
	// saved. A durable prepared operation is therefore never safe to replay.
	next := cloneCheckpoint(checkpoint)
	next.Outbound.Phase = OutboundUnknown
	verified, err := loop.saveVerified(ctx, stored, next)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	return StoredCheckpoint{}, fmt.Errorf(
		"%w for operation %q",
		ErrDeliveryUnknown,
		verified.Checkpoint.Outbound.OperationID,
	)
}

// ListUnknown returns the single bounded outer mutation fence, if present.
func (loop *Loop) ListUnknown(ctx context.Context) ([]UnknownOutbound, error) {
	stored, found, err := loop.store.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !found || stored.Checkpoint.Outbound == nil || stored.Checkpoint.Outbound.Phase != OutboundUnknown {
		return []UnknownOutbound{}, nil
	}
	outbound := stored.Checkpoint.Outbound
	return []UnknownOutbound{{OperationID: outbound.OperationID, UpdateID: outbound.UpdateID, Status: outbound.Status}}, nil
}

// ConfirmUnknownOutbound resolves only from the inner durable sender receipt.
func (loop *Loop) ConfirmUnknownOutbound(ctx context.Context, operationID string, updateID int64) (StoredCheckpoint, error) {
	loop.mutation.Lock()
	defer loop.mutation.Unlock()
	stored, err := loop.loadExactUnknown(ctx, operationID, updateID)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	receipt, confirmed, err := loop.resolveOutboundReceipt(ctx, operationID)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	if !confirmed {
		return StoredCheckpoint{}, ErrDeliveryUnknown
	}
	return loop.confirmOutbound(ctx, stored, receipt)
}

// ConfirmEnqueuedOutbound projects a verified inner delivery receipt into the
// outer checkpoint without changing or regressing the already advanced offset.
func (loop *Loop) ConfirmEnqueuedOutbound(ctx context.Context, operationID string, updateID int64) (StoredCheckpoint, error) {
	loop.mutation.Lock()
	defer loop.mutation.Unlock()
	stored, found, err := loop.store.Load(ctx)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	if !found || stored.Checkpoint.Outbound == nil || stored.Checkpoint.Outbound.Phase != OutboundEnqueued ||
		stored.Checkpoint.Outbound.OperationID != operationID || stored.Checkpoint.Outbound.UpdateID != updateID {
		return StoredCheckpoint{}, ErrDeliveryUnknown
	}
	receipt, confirmed, err := loop.resolveOutboundReceipt(ctx, operationID)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	if !confirmed {
		return StoredCheckpoint{}, ErrDeliveryUnknown
	}
	return loop.confirmOutbound(ctx, stored, receipt)
}

// RetryUnknownOutbound performs exactly one explicit owner-authorized send.
// Any ambiguous result remains unknown and is never retried automatically.
func (loop *Loop) RetryUnknownOutbound(ctx context.Context, operationID string, updateID int64) (StoredCheckpoint, error) {
	loop.mutation.Lock()
	defer loop.mutation.Unlock()
	stored, err := loop.loadExactUnknown(ctx, operationID, updateID)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	outbound := stored.Checkpoint.Outbound
	var receipt Receipt
	var sendErr error
	if editor, ok := loop.sender.(CarrierEditor); ok && outbound.Status.SourceMessageID > 0 {
		receipt, sendErr = editor.EditStatusWithKeyboard(ctx, operationID, outbound.Status, outbound.Keyboard)
	} else if keyboardSender, ok := loop.sender.(KeyboardSender); ok {
		receipt, sendErr = keyboardSender.SendStatusWithKeyboard(ctx, operationID, outbound.Status, outbound.Keyboard)
	} else {
		receipt, sendErr = loop.sender.SendStatus(ctx, operationID, outbound.Status)
	}
	if sendErr != nil || receipt.MessageID <= 0 {
		return StoredCheckpoint{}, fmt.Errorf("%w for operation %q", ErrDeliveryUnknown, operationID)
	}
	return loop.confirmOutbound(ctx, stored, receipt)
}
func (loop *Loop) loadExactUnknown(ctx context.Context, operationID string, updateID int64) (StoredCheckpoint, error) {
	stored, found, err := loop.store.Load(ctx)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	if !found || stored.Checkpoint.Outbound == nil || stored.Checkpoint.Outbound.Phase != OutboundUnknown ||
		stored.Checkpoint.Outbound.OperationID != operationID || stored.Checkpoint.Outbound.UpdateID != updateID {
		return StoredCheckpoint{}, ErrDeliveryUnknown
	}
	return stored, nil
}
func (loop *Loop) resolveOutboundReceipt(ctx context.Context, operationID string) (Receipt, bool, error) {
	resolver, ok := loop.sender.(OutboundReceiptResolver)
	if !ok {
		return Receipt{}, false, nil
	}
	receipt, found, err := resolver.ResolveStatusReceipt(ctx, operationID)
	if err != nil {
		return Receipt{}, false, err
	}
	if found && receipt.MessageID <= 0 {
		return Receipt{}, false, ErrDeliveryUnknown
	}
	return receipt, found, nil
}
func (loop *Loop) confirmOutbound(ctx context.Context, stored StoredCheckpoint, receipt Receipt) (StoredCheckpoint, error) {
	if receipt.MessageID <= 0 || stored.Checkpoint.Outbound == nil {
		return StoredCheckpoint{}, ErrDeliveryUnknown
	}
	next := cloneCheckpoint(stored.Checkpoint)
	if next.NextUpdateID <= next.Outbound.UpdateID {
		next.NextUpdateID = next.Outbound.UpdateID + 1
	}
	next.Outbound.Phase = OutboundConfirmed
	next.Outbound.Receipt = &Receipt{MessageID: receipt.MessageID}
	next.Outbound.Durable = nil
	return loop.saveVerified(context.WithoutCancel(ctx), stored, next)
}
func (loop *Loop) handleUpdate(
	ctx context.Context,
	stored StoredCheckpoint,
	update Update,
) (StoredCheckpoint, error) {
	loop.mutation.Lock()
	defer loop.mutation.Unlock()
	latest, found, err := loop.store.Load(ctx)
	if err != nil {
		return StoredCheckpoint{}, fmt.Errorf("refresh coordinator checkpoint: %w", err)
	}
	if !found || validateStored(latest) != nil {
		return StoredCheckpoint{}, ErrCheckpointMismatch
	}
	if latest.Revision != stored.Revision {
		if latest.Checkpoint.NextUpdateID != stored.Checkpoint.NextUpdateID {
			return StoredCheckpoint{}, ErrCheckpointMismatch
		}
		stored = latest
	}
	decision, err := loop.handler.Handle(ctx, update)
	if err != nil {
		if recovery, ok := loop.handler.(UnknownRecoveryHandler); ok {
			control, prompt, recoveryErr := recovery.PrepareUnknownRecovery(ctx, update)
			if recoveryErr != nil {
				return StoredCheckpoint{}, fmt.Errorf("prepare recovery for update %d: %w", update.ID, recoveryErr)
			}
			if err := recoverycontrol.Validate(control, update.ID); err != nil {
				return StoredCheckpoint{}, fmt.Errorf("prepare recovery for update %d: %w", update.ID, err)
			}
			if err := validateDecision(prompt); err != nil || prompt.Kind != DecisionStatus {
				if err == nil {
					err = errors.New("recovery prompt must be a status decision")
				}
				return StoredCheckpoint{}, fmt.Errorf("prepare recovery for update %d: %w", update.ID, err)
			}
			return loop.enqueueRecoveryPrompt(ctx, stored, update, control, prompt)
		}
		return StoredCheckpoint{}, fmt.Errorf("handle update %d: %w", update.ID, err)
	}
	if err := validateDecision(decision); err != nil {
		return StoredCheckpoint{}, fmt.Errorf("handle update %d: %w", update.ID, err)
	}
	switch decision.Kind {
	case DecisionSkip:
		next := cloneCheckpoint(stored.Checkpoint)
		next.NextUpdateID = update.ID + 1
		return loop.saveVerified(ctx, stored, next)
	case DecisionBlock:
		next := cloneCheckpoint(stored.Checkpoint)
		next.Blocked = &BlockedUpdate{UpdateID: update.ID, Reason: decision.BlockReason}
		if _, err := loop.saveVerified(ctx, stored, next); err != nil {
			return StoredCheckpoint{}, err
		}
		return StoredCheckpoint{}, fmt.Errorf("%w at update %d", ErrUpdateBlocked, update.ID)
	case DecisionStatus:
		return loop.sendStatus(ctx, stored, update, decision.Status, decision.Keyboard)
	default:
		panic("validated decision has unsupported kind")
	}
}
func (loop *Loop) sendStatus(
	ctx context.Context,
	stored StoredCheckpoint,
	update Update,
	status Status,
	keyboard *KeyboardMarkup,
) (StoredCheckpoint, error) {
	operationID := "status:" + strconv.FormatInt(update.ID, 10)
	next := cloneCheckpoint(stored.Checkpoint)
	next.Outbound = &Outbound{
		OperationID: operationID,
		UpdateID:    update.ID,
		Status:      status,
		Keyboard:    cloneKeyboard(keyboard),
		Phase:       OutboundPrepared,
	}
	prepared, err := loop.saveVerified(ctx, stored, next)
	if err != nil {
		return StoredCheckpoint{}, err
	}
	if durable, ok := loop.sender.(DurableStatusSender); ok {
		receipt, enqueueErr := durable.EnqueueStatus(ctx, operationID, status, keyboard)
		if enqueueErr != nil || receipt.OperationID != operationID || receipt.Sequence == 0 {
			unknown := cloneCheckpoint(prepared.Checkpoint)
			unknown.Outbound.Phase = OutboundUnknown
			if _, err := loop.saveVerified(context.WithoutCancel(ctx), prepared, unknown); err != nil {
				return StoredCheckpoint{}, err
			}
			return StoredCheckpoint{}, fmt.Errorf("%w for durable operation %q", ErrDeliveryUnknown, operationID)
		}
		enqueued := cloneCheckpoint(prepared.Checkpoint)
		enqueued.NextUpdateID = update.ID + 1
		enqueued.Outbound.Phase = OutboundEnqueued
		enqueued.Outbound.Durable = &DurableOutboundReceipt{OperationID: receipt.OperationID, Sequence: receipt.Sequence}
		return loop.saveVerified(context.WithoutCancel(ctx), prepared, enqueued)
	}
	var receipt Receipt
	var sendErr error
	if editor, ok := loop.sender.(CarrierEditor); ok && status.SourceMessageID > 0 {
		receipt, sendErr = editor.EditStatusWithKeyboard(ctx, operationID, status, keyboard)
	} else if keyboardSender, ok := loop.sender.(KeyboardSender); ok {
		receipt, sendErr = keyboardSender.SendStatusWithKeyboard(ctx, operationID, status, keyboard)
	} else {
		receipt, sendErr = loop.sender.SendStatus(ctx, operationID, status)
	}
	if sendErr != nil || receipt.MessageID <= 0 {
		unknown := cloneCheckpoint(prepared.Checkpoint)
		unknown.Outbound.Phase = OutboundUnknown
		if _, err := loop.saveVerified(ctx, prepared, unknown); err != nil {
			return StoredCheckpoint{}, err
		}
		return StoredCheckpoint{}, fmt.Errorf("%w for operation %q", ErrDeliveryUnknown, operationID)
	}
	confirmed := cloneCheckpoint(prepared.Checkpoint)
	confirmed.NextUpdateID = update.ID + 1
	confirmed.Outbound.Phase = OutboundConfirmed
	confirmed.Outbound.Receipt = &Receipt{MessageID: receipt.MessageID}
	return loop.saveVerified(ctx, prepared, confirmed)
}
func (loop *Loop) saveVerified(
	ctx context.Context,
	current StoredCheckpoint,
	next Checkpoint,
) (StoredCheckpoint, error) {
	if err := validateCheckpoint(next); err != nil {
		return StoredCheckpoint{}, err
	}
	saved, err := loop.store.Save(ctx, current.Revision, cloneCheckpoint(next))
	if err != nil {
		return StoredCheckpoint{}, fmt.Errorf("save coordinator checkpoint: %w", err)
	}
	if saved.Revision <= current.Revision || !reflect.DeepEqual(saved.Checkpoint, next) {
		return StoredCheckpoint{}, ErrCheckpointMismatch
	}
	reread, found, err := loop.store.Load(ctx)
	if err != nil {
		return StoredCheckpoint{}, fmt.Errorf("reread coordinator checkpoint: %w", err)
	}
	if !found || !reflect.DeepEqual(reread, saved) {
		return StoredCheckpoint{}, ErrCheckpointMismatch
	}
	if err := validateStored(reread); err != nil {
		return StoredCheckpoint{}, err
	}
	return reread, nil
}
func validateStored(stored StoredCheckpoint) error {
	if stored.Revision == 0 {
		return fmt.Errorf("%w: stored revision must be positive", ErrInvalidCheckpoint)
	}
	return validateCheckpoint(stored.Checkpoint)
}
func validateCheckpoint(checkpoint Checkpoint) error {
	if !checkpoint.Initialized || checkpoint.NextUpdateID < 0 {
		return fmt.Errorf("%w: initialized checkpoint and non-negative next update id are required", ErrInvalidCheckpoint)
	}
	if checkpoint.Blocked != nil {
		if checkpoint.Blocked.UpdateID <= 0 || checkpoint.Blocked.UpdateID < checkpoint.NextUpdateID ||
			strings.TrimSpace(checkpoint.Blocked.Reason) == "" {
			return fmt.Errorf("%w: invalid blocked update", ErrInvalidCheckpoint)
		}
	}
	if checkpoint.Recovery != nil {
		if err := recoverycontrol.Validate(*checkpoint.Recovery, checkpoint.Recovery.UpdateID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
		}
		if checkpoint.Recovery.UpdateID > checkpoint.NextUpdateID {
			return fmt.Errorf("%w: recovery update exceeds checkpoint offset", ErrInvalidCheckpoint)
		}
		if checkpoint.Recovery.UpdateID == checkpoint.NextUpdateID && (checkpoint.Outbound == nil ||
			checkpoint.Outbound.OperationID != checkpoint.Recovery.PromptOperationID || checkpoint.Outbound.UpdateID != checkpoint.Recovery.UpdateID ||
			(checkpoint.Outbound.Phase != OutboundPrepared && checkpoint.Outbound.Phase != OutboundUnknown)) {
			return fmt.Errorf("%w: recovery control has not been durably accepted", ErrInvalidCheckpoint)
		}
	}
	if checkpoint.Outbound == nil {
		return nil
	}
	outbound := checkpoint.Outbound
	if strings.TrimSpace(outbound.OperationID) == "" || outbound.UpdateID <= 0 ||
		outbound.Status.ConversationID <= 0 || strings.TrimSpace(outbound.Status.Text) == "" {
		return fmt.Errorf("%w: invalid outbound operation", ErrInvalidCheckpoint)
	}
	switch outbound.Phase {
	case OutboundPrepared, OutboundUnknown:
		if outbound.Receipt != nil || outbound.Durable != nil || outbound.UpdateID < checkpoint.NextUpdateID {
			return fmt.Errorf("%w: unresolved outbound has invalid receipt or offset", ErrInvalidCheckpoint)
		}
	case OutboundEnqueued:
		if outbound.Receipt != nil || outbound.Durable == nil || outbound.Durable.OperationID != outbound.OperationID ||
			outbound.Durable.Sequence == 0 || outbound.UpdateID == math.MaxInt64 || checkpoint.NextUpdateID <= outbound.UpdateID {
			return fmt.Errorf("%w: enqueued outbound has invalid durable receipt or offset", ErrInvalidCheckpoint)
		}
	case OutboundConfirmed:
		if outbound.Receipt == nil || outbound.Durable != nil || outbound.Receipt.MessageID <= 0 ||
			outbound.UpdateID == math.MaxInt64 || checkpoint.NextUpdateID <= outbound.UpdateID {
			return fmt.Errorf("%w: confirmed outbound has invalid receipt or offset", ErrInvalidCheckpoint)
		}
	default:
		return fmt.Errorf("%w: unsupported outbound phase %q", ErrInvalidCheckpoint, outbound.Phase)
	}
	return nil
}
func validateUpdates(nextUpdateID int64, updates []Update) error {
	previous := int64(0)
	for index, update := range updates {
		if update.ID < nextUpdateID || update.ID <= 0 || update.ID == math.MaxInt64 ||
			(index > 0 && update.ID <= previous) {
			return fmt.Errorf("%w at batch index %d", ErrInvalidUpdateSequence, index)
		}
		if err := validateUpdatePayload(update); err != nil {
			return fmt.Errorf("%w at batch index %d: %v", ErrInvalidUpdateSequence, index, err)
		}
		previous = update.ID
	}
	return nil
}
func validateUpdatePayload(update Update) error {
	switch update.Kind {
	case UpdateUnsupported:
		payload := update
		payload.ID = 0
		payload.Kind = UpdateUnsupported
		if payload != (Update{}) {
			return errors.New("unsupported update must have an empty payload")
		}
	case UpdateMessage:
		if update.ActorID <= 0 || update.ConversationID == 0 || strings.TrimSpace(update.ConversationKind) == "" {
			return errors.New("message update requires actor and conversation identity")
		}
		if update.CallbackQueryID != "" {
			return errors.New("message update cannot contain callback query identity")
		}
		if update.SourceMessageID < 0 || update.ReplyToMessageID < 0 ||
			(update.ReplyToMessageID > 0 && update.SourceMessageID == 0) {
			return errors.New("message update has invalid source or reply message identity")
		}
		if err := validateMessageMedia(update); err != nil {
			return err
		}
	case UpdateCallback:
		if update.ActorID <= 0 || update.ConversationID == 0 || strings.TrimSpace(update.ConversationKind) == "" {
			return errors.New("callback update requires actor and conversation identity")
		}
		if strings.TrimSpace(update.Text) == "" || strings.TrimSpace(update.CallbackQueryID) == "" ||
			update.SourceMessageID <= 0 {
			return errors.New("callback update requires data, query id, and positive source message id")
		}
		if update.Caption != "" || update.ReplyToMessageID != 0 || update.MediaKind != "" ||
			update.MediaFileID != "" || update.MediaFileUniqueID != "" || update.MediaFileSize != 0 ||
			update.MediaMIMEType != "" || update.MediaDurationSeconds != 0 || update.MediaWidth != 0 ||
			update.MediaHeight != 0 || update.MediaDownloadAllowed {
			return errors.New("callback update cannot contain message media metadata")
		}
	default:
		return fmt.Errorf("unsupported update kind %q", update.Kind)
	}
	return nil
}
func validateMessageMedia(update Update) error {
	if update.MediaFileSize < 0 || update.MediaDurationSeconds < 0 || update.MediaWidth < 0 || update.MediaHeight < 0 {
		return errors.New("message media dimensions and sizes must not be negative")
	}
	if update.MediaKind == "" {
		if update.Caption != "" || update.MediaFileID != "" || update.MediaFileUniqueID != "" ||
			update.MediaFileSize != 0 || update.MediaMIMEType != "" || update.MediaDurationSeconds != 0 ||
			update.MediaWidth != 0 || update.MediaHeight != 0 || update.MediaDownloadAllowed {
			return errors.New("message media metadata requires a media kind")
		}
		return nil
	}
	if strings.TrimSpace(update.MediaKind) != update.MediaKind || strings.TrimSpace(update.MediaFileID) == "" {
		return errors.New("message media requires a normalized kind and file id")
	}
	if update.MediaDownloadAllowed && update.MediaKind != "voice" && update.MediaKind != "photo" {
		return errors.New("only voice and photo media may be downloaded")
	}
	if update.MediaKind == "video" && update.MediaDownloadAllowed {
		return errors.New("video download must remain disabled")
	}
	return nil
}
func validateDecision(decision Decision) error {
	switch decision.Kind {
	case DecisionSkip:
		if decision.Status != (Status{}) || decision.BlockReason != "" {
			return errors.New("skip decision must not contain status or block data")
		}
	case DecisionStatus:
		if decision.Status.ConversationID <= 0 || strings.TrimSpace(decision.Status.Text) == "" ||
			decision.BlockReason != "" {
			return errors.New("status decision requires a destination and non-empty safe text")
		}
	case DecisionBlock:
		if strings.TrimSpace(decision.BlockReason) == "" || decision.Status != (Status{}) {
			return errors.New("block decision requires a reason and no status")
		}
	default:
		return fmt.Errorf("unsupported decision kind %q", decision.Kind)
	}
	return nil
}
func cloneCheckpoint(checkpoint Checkpoint) Checkpoint {
	if checkpoint.Blocked != nil {
		blocked := *checkpoint.Blocked
		checkpoint.Blocked = &blocked
	}
	if checkpoint.Outbound != nil {
		outbound := *checkpoint.Outbound
		outbound.Keyboard = cloneKeyboard(outbound.Keyboard)
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
	if checkpoint.Recovery != nil {
		recovery := *checkpoint.Recovery
		checkpoint.Recovery = &recovery
	}
	return checkpoint
}
func cloneKeyboard(keyboard *KeyboardMarkup) *KeyboardMarkup {
	if keyboard == nil {
		return nil
	}
	clone := make(KeyboardMarkup, len(*keyboard))
	for row := range *keyboard {
		clone[row] = append([]KeyboardButton(nil), (*keyboard)[row]...)
	}
	return &clone
}
