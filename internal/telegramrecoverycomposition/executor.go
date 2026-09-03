// Package telegramrecoverycomposition resolves signed owner recovery clicks
// against exact durable Telegram operations and requests a fresh projection.
package telegramrecoverycomposition

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"bria/internal/coordinator"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecovery/statusrecovery"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

var ErrInvalidOptions = errors.New("Telegram recovery composition is unavailable")

type ProjectionRequest struct {
	Scope       statusrecovery.Scope
	Carrier     telegramstate.Carrier
	OperationID string
	Sequence    uint64
}

type Projector interface {
	ProjectCurrent(context.Context, ProjectionRequest) (telegramflow.CallbackResult, error)
}

type StatusResolver interface {
	ConfirmUnknownStatus(context.Context, string, coordinator.Receipt) error
	RetryUnknownStatus(context.Context, string) error
	EnqueueRecoveryStatus(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup, telegramflow.StatusRecoveryBinding) (coordinator.DurableOutboundReceipt, error)
	ConfirmUnknownSend(context.Context, string, coordinator.Receipt) error
	RetryUnknownSend(context.Context, string) error
	SendStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
	EditStatusWithKeyboard(context.Context, string, coordinator.Status, *coordinator.KeyboardMarkup) (coordinator.Receipt, error)
}

type Executor struct {
	next       telegramflow.CallbackExecutor
	operations telegramflow.CallbackOperationStore
	projector  Projector
	statuses   StatusResolver
}

func New(next telegramflow.CallbackExecutor, operations telegramflow.CallbackOperationStore, projector Projector) (*Executor, error) {
	if next == nil || operations == nil || projector == nil {
		return nil, ErrInvalidOptions
	}
	return &Executor{next: next, operations: operations, projector: projector}, nil
}

func (executor *Executor) Bind(statuses StatusResolver) error {
	if executor == nil || statuses == nil {
		return ErrInvalidOptions
	}
	executor.statuses = statuses
	return nil
}

func (executor *Executor) HandleCallback(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	if plan.Recovery == nil && plan.StatusRecovery == nil {
		return executor.next.HandleCallback(ctx, plan)
	}
	if executor == nil || executor.statuses == nil || ctx == nil || plan.Recovery != nil && plan.StatusRecovery != nil {
		return telegramflow.CallbackResult{}, ErrInvalidOptions
	}
	if plan.StatusRecovery != nil {
		return executor.resolveStatus(ctx, plan)
	}
	return executor.resolveCallback(ctx, plan)
}

func (executor *Executor) resolveCallback(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	recovery := plan.Recovery
	if recovery == nil || recovery.Decision != plan.Action || !validCallbackDecision(plan.Action, plan.Effect, recovery.Phase) ||
		plan.SessionID != telegramui.GlobalSurfaceID || plan.Target != (telegramui.ButtonTarget{}) {
		return telegramflow.CallbackResult{}, ErrInvalidOptions
	}
	operation, found, err := executor.operations.Load(ctx, recovery.OperationID)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	wantPhase := telegramflow.CallbackEffectUnknown
	if recovery.Phase == telegrampipeline.CallbackSendUnknownPhase {
		wantPhase = telegramflow.CallbackSendUnknown
	} else if recovery.Phase == telegrampipeline.CallbackEffectRetryUnknownPhase {
		wantPhase = telegramflow.CallbackEffectRetryUnknown
	}
	if !found || operation.Phase != wantPhase || operation.ID != recovery.OperationID || operation.UpdateID != recovery.UpdateID ||
		operation.Plan.SessionID != recovery.SessionID || operation.Plan.Carrier != recovery.Carrier {
		return telegramflow.CallbackResult{}, errors.New("callback recovery no longer matches the exact unknown operation")
	}
	switch plan.Action {
	case telegramui.ActionCallbackEffectConfirmed:
		err = executor.confirmCallbackEffect(ctx, operation)
	case telegramui.ActionCallbackEffectRetryPossibleDuplicate:
		err = executor.retryCallbackEffect(ctx, operation)
	case telegramui.ActionCallbackSendConfirmed:
		err = executor.statuses.ConfirmUnknownSend(ctx, operation.ID, coordinator.Receipt{MessageID: recovery.Carrier.MessageID})
	case telegramui.ActionCallbackSendRetryPossibleDuplicate:
		err = executor.retryCallbackSend(ctx, operation)
	default:
		return telegramflow.CallbackResult{}, ErrInvalidOptions
	}
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	scope := statusrecovery.Scope{Kind: statusrecovery.ScopeSession, SessionID: recovery.SessionID}
	if recovery.SessionID == telegramui.GlobalSurfaceID {
		scope = statusrecovery.Scope{Kind: statusrecovery.ScopeGlobal}
	}
	return executor.project(ctx, plan, scope, operation.ID, uint64(operation.UpdateID))
}

func (executor *Executor) confirmCallbackEffect(ctx context.Context, operation telegramflow.CallbackOperation) error {
	resolved := operation
	resolved.Phase = telegramflow.CallbackEffectResolved
	changed, err := executor.operations.CompareAndSwap(ctx, operation.ID, operation.Phase, resolved)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback effect changed while confirming")
	}
	return nil
}

func (executor *Executor) retryCallbackEffect(ctx context.Context, operation telegramflow.CallbackOperation) error {
	fenced := operation
	fenced.Phase = telegramflow.CallbackEffectRetryUnknown
	changed, err := executor.operations.CompareAndSwap(ctx, operation.ID, telegramflow.CallbackEffectUnknown, fenced)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback effect changed while fencing explicit retry")
	}
	result, err := executor.next.HandleCallback(ctx, operation.Plan)
	if err != nil {
		return fmt.Errorf("%w: retried callback effect: %v", telegrampipeline.ErrUnknownOperation, err)
	}
	if result.OperationID != operation.ID || resultCount(result) != 1 {
		return fmt.Errorf("%w: retried callback result is invalid", telegrampipeline.ErrUnknownOperation)
	}
	resolved := fenced
	resolved.Phase = telegramflow.CallbackEffectResolved
	changed, err = executor.operations.CompareAndSwap(context.WithoutCancel(ctx), operation.ID, telegramflow.CallbackEffectRetryUnknown, resolved)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: retried callback resolution changed", telegrampipeline.ErrUnknownOperation)
	}
	return nil
}

func (executor *Executor) retryCallbackSend(ctx context.Context, operation telegramflow.CallbackOperation) error {
	if operation.Prepared == nil {
		return errors.New("unknown callback send has no prepared output")
	}
	if err := executor.statuses.RetryUnknownSend(ctx, operation.ID); err != nil {
		return err
	}
	var receipt coordinator.Receipt
	var err error
	if operation.Prepared.Edit {
		receipt, err = executor.statuses.EditStatusWithKeyboard(ctx, operation.ID, operation.Prepared.Status, operation.Prepared.Keyboard)
	} else {
		receipt, err = executor.statuses.SendStatusWithKeyboard(ctx, operation.ID, operation.Prepared.Status, operation.Prepared.Keyboard)
	}
	if err != nil || receipt.MessageID <= 0 {
		return fmt.Errorf("%w: explicit callback send retry %s", telegrampipeline.ErrUnknownOperation, operation.ID)
	}
	current, found, loadErr := executor.operations.Load(context.WithoutCancel(ctx), operation.ID)
	if loadErr != nil {
		return loadErr
	}
	if !found || current.Phase != telegramflow.CallbackCommitted {
		return errors.New("explicit callback send retry did not reach an exact durable outcome")
	}
	return nil
}

func validCallbackDecision(action telegramui.Action, effect telegrampipeline.CallbackEffect, phase string) bool {
	switch action {
	case telegramui.ActionCallbackEffectConfirmed:
		return (phase == telegrampipeline.CallbackEffectUnknownPhase || phase == telegrampipeline.CallbackEffectRetryUnknownPhase) && effect == telegrampipeline.EffectCallbackEffectConfirmed
	case telegramui.ActionCallbackEffectRetryPossibleDuplicate:
		return phase == telegrampipeline.CallbackEffectUnknownPhase && effect == telegrampipeline.EffectCallbackEffectRetryPossibleDuplicate
	case telegramui.ActionCallbackSendConfirmed:
		return phase == telegrampipeline.CallbackSendUnknownPhase && effect == telegrampipeline.EffectCallbackSendConfirmed
	case telegramui.ActionCallbackSendRetryPossibleDuplicate:
		return phase == telegrampipeline.CallbackSendUnknownPhase && effect == telegrampipeline.EffectCallbackSendRetryPossibleDuplicate
	default:
		return false
	}
}

func (executor *Executor) resolveStatus(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	recovery := plan.StatusRecovery
	if recovery == nil || !validStatusDecision(plan.Action, plan.Effect) || recovery.Decision != plan.Action ||
		plan.SessionID != telegramui.GlobalSurfaceID || plan.Target != (telegramui.ButtonTarget{}) || !statusrecovery.Valid(recovery.Binding) {
		return telegramflow.CallbackResult{}, ErrInvalidOptions
	}
	operation, found, err := executor.operations.LoadStatus(ctx, recovery.Binding.OperationID)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	if !found || operation.Phase != telegramflow.StatusSendUnknown || operation.Recovery == nil ||
		!reflect.DeepEqual(*operation.Recovery, recovery.Binding) || operation.Sequence != recovery.Binding.Sequence ||
		operation.Edit != recovery.Binding.Edit || (operation.Prepared != nil) != recovery.Binding.Prepared {
		return telegramflow.CallbackResult{}, errors.New("status recovery no longer matches the exact unknown operation")
	}
	switch plan.Action {
	case telegramui.ActionStatusRecoveryAssumeDelivered:
		err = executor.statuses.ConfirmUnknownStatus(ctx, operation.ID, coordinator.Receipt{MessageID: recovery.Binding.Carrier.MessageID})
	case telegramui.ActionStatusRecoveryRetryPossibleDuplicate:
		err = executor.retryStatus(ctx, operation, recovery.Binding)
	case telegramui.ActionStatusRecoveryCancel:
	default:
		return telegramflow.CallbackResult{}, ErrInvalidOptions
	}
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	return executor.project(ctx, plan, recovery.Binding.Scope, recovery.Binding.OperationID, recovery.Binding.Sequence)
}

func (executor *Executor) retryStatus(ctx context.Context, operation telegramflow.StatusOperation, binding telegramflow.StatusRecoveryBinding) error {
	if operation.Prepared != nil {
		callback, found, err := executor.operations.Load(ctx, operation.ID)
		if err != nil {
			return err
		}
		if found {
			if callback.Phase != telegramflow.CallbackSendUnknown || callback.Prepared == nil || !reflect.DeepEqual(callback.Prepared, operation.Prepared) {
				return errors.New("prepared status callback recovery no longer matches")
			}
			if err := executor.statuses.RetryUnknownSend(ctx, operation.ID); err != nil {
				return err
			}
		}
	}
	if err := executor.statuses.RetryUnknownStatus(ctx, operation.ID); err != nil {
		return err
	}
	if _, err := executor.statuses.EnqueueRecoveryStatus(ctx, operation.ID, operation.Status, operation.Keyboard, binding); err != nil {
		return err
	}
	current, found, err := executor.operations.LoadStatus(context.WithoutCancel(ctx), operation.ID)
	if err != nil {
		return err
	}
	if !found || current.Phase == telegramflow.StatusSendUnknown {
		return fmt.Errorf("%w: explicit status retry %s", telegrampipeline.ErrUnknownOperation, operation.ID)
	}
	if current.Phase != telegramflow.StatusCommitted {
		return errors.New("explicit status retry did not reach an exact durable outcome")
	}
	return nil
}

func (executor *Executor) project(ctx context.Context, plan telegrampipeline.CallbackPlan, scope statusrecovery.Scope, operationID string, sequence uint64) (telegramflow.CallbackResult, error) {
	result, err := executor.projector.ProjectCurrent(ctx, ProjectionRequest{Scope: scope, Carrier: plan.Carrier, OperationID: operationID, Sequence: sequence})
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	if resultCount(result) != 1 {
		return telegramflow.CallbackResult{}, errors.New("current recovery projection must return exactly one output")
	}
	result.OperationID = plan.OperationID
	return result, nil
}

func validStatusDecision(action telegramui.Action, effect telegrampipeline.CallbackEffect) bool {
	switch action {
	case telegramui.ActionStatusRecoveryAssumeDelivered:
		return effect == telegrampipeline.EffectStatusRecoveryAssumeDelivered
	case telegramui.ActionStatusRecoveryRetryPossibleDuplicate:
		return effect == telegrampipeline.EffectStatusRecoveryRetryPossibleDuplicate
	case telegramui.ActionStatusRecoveryCancel:
		return effect == telegrampipeline.EffectStatusRecoveryCancel
	default:
		return false
	}
}

func resultCount(result telegramflow.CallbackResult) int {
	count := 0
	if result.Card != nil {
		count++
	}
	if result.Surface != nil {
		count++
	}
	if result.Terminal != nil {
		count++
	}
	return count
}

var _ telegramflow.CallbackExecutor = (*Executor)(nil)
