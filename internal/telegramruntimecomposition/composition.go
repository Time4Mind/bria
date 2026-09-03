// Package telegramruntimecomposition projects typed controller actions into
// the signed Telegram runtime and reconciles its durable delivery receipts.
package telegramruntimecomposition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecoverycomposition"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type SemanticController interface {
	HandleSemanticMessage(context.Context, coordinator.Update) (telegramcontroller.SemanticActionResult, error)
	HandleSemanticAction(context.Context, telegramcontroller.SemanticAction) (telegramcontroller.SemanticActionResult, error)
}

type currentController interface {
	ProjectCurrent(context.Context, domain.SessionID) (telegramcontroller.SemanticActionResult, error)
}

// CurrentProjector renders a recovery projection without applying a semantic action.
type CurrentProjector struct{ Controller currentController }

func (projector CurrentProjector) ProjectCurrent(ctx context.Context, request telegramrecoverycomposition.ProjectionRequest) (telegramflow.CallbackResult, error) {
	if projector.Controller == nil || ctx == nil {
		return telegramflow.CallbackResult{}, errors.New("current semantic Telegram controller is required")
	}
	sessionID := request.Scope.SessionID
	result, err := projector.Controller.ProjectCurrent(ctx, sessionID)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	card, surface, err := projectSemanticResult(result, telegramui.EffectEditSameCarrier)
	if err != nil || (card == nil) == (surface == nil) {
		if err == nil {
			err = errors.New("current recovery projection must contain exactly one output")
		}
		return telegramflow.CallbackResult{}, err
	}
	return telegramflow.CallbackResult{OperationID: request.OperationID, Card: card, Surface: surface}, nil
}

type ControllerFlowAdapter struct{ Controller SemanticController }

func (adapter ControllerFlowAdapter) HandleMessage(ctx context.Context, update coordinator.Update) (telegramflow.MessageResult, error) {
	if adapter.Controller == nil {
		return telegramflow.MessageResult{}, errors.New("semantic Telegram controller is required")
	}
	result, err := adapter.Controller.HandleSemanticMessage(ctx, update)
	if err != nil {
		return telegramflow.MessageResult{}, err
	}
	if result.Card == nil && result.Surface != nil && len(result.Surface.Rows) == 0 {
		if result.Decision.Keyboard != nil {
			return telegramflow.MessageResult{}, errors.New("unsigned Telegram keyboard rejected")
		}
		return telegramflow.MessageResult{Decision: result.Decision}, nil
	}
	card, surface, err := projectSemanticResult(result, telegramui.EffectSendOneNewCard)
	if err != nil {
		return telegramflow.MessageResult{}, err
	}
	return telegramflow.MessageResult{Decision: result.Decision, Card: card, Surface: surface}, nil
}

func (adapter ControllerFlowAdapter) HandleCallback(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	if adapter.Controller == nil {
		return telegramflow.CallbackResult{}, errors.New("semantic Telegram controller is required")
	}
	action, err := semanticActionFromPlan(plan)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	result, err := adapter.Controller.HandleSemanticAction(ctx, action)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	card, surface, err := projectSemanticResult(result, telegramui.EffectEditSameCarrier)
	if err != nil {
		return telegramflow.CallbackResult{}, err
	}
	if (card == nil) == (surface == nil) {
		return telegramflow.CallbackResult{}, errors.New("semantic callback must return exactly one card or surface")
	}
	return telegramflow.CallbackResult{OperationID: plan.OperationID, Card: card, Surface: surface}, nil
}

func semanticActionFromPlan(plan telegrampipeline.CallbackPlan) (telegramcontroller.SemanticAction, error) {
	if plan.OperationID == "" || plan.UpdateID <= 0 {
		return telegramcontroller.SemanticAction{}, errors.New("callback plan identity is required")
	}
	var kind telegramcontroller.SemanticActionKind
	switch plan.Action {
	case telegramui.ActionPagePrevious:
		kind = telegramcontroller.SemanticPagePrevious
	case telegramui.ActionPageLatest:
		kind = telegramcontroller.SemanticPageLatest
	case telegramui.ActionPageNext:
		kind = telegramcontroller.SemanticPageNext
	case telegramui.ActionStop:
		kind = telegramcontroller.SemanticStop
	case telegramui.ActionClose:
		kind = telegramcontroller.SemanticClose
	case telegramui.ActionOptions:
		kind = telegramcontroller.SemanticOptions
	case telegramui.ActionScreen:
		kind = telegramcontroller.SemanticScreen
	case telegramui.ActionSelectSession:
		kind = telegramcontroller.SemanticSelect
	case telegramui.ActionResume:
		kind = telegramcontroller.SemanticResume
	case telegramui.ActionMenuSessions:
		kind = telegramcontroller.SemanticMenuSessions
	case telegramui.ActionMenuNew:
		kind = telegramcontroller.SemanticMenuNew
	case telegramui.ActionMenuArchive:
		kind = telegramcontroller.SemanticMenuArchive
	case telegramui.ActionMenuStatus:
		kind = telegramcontroller.SemanticMenuStatus
	case telegramui.ActionMenuSettings:
		kind = telegramcontroller.SemanticMenuSettings
	case telegramui.ActionMenuBack:
		kind = telegramcontroller.SemanticMenuBack
	case telegramui.ActionCreateCodex:
		kind = telegramcontroller.SemanticCreateCodex
	case telegramui.ActionCreateClaude:
		kind = telegramcontroller.SemanticCreateClaude
	case telegramui.ActionSettingsScreen:
		kind = telegramcontroller.SemanticSettingsScreen
	case telegramui.ActionSettingsDetail:
		kind = telegramcontroller.SemanticSettingsDetail
	case telegramui.ActionAuthorizeCodex:
		kind = telegramcontroller.SemanticAuthorizeCodex
	case telegramui.ActionAuthorizeClaude:
		kind = telegramcontroller.SemanticAuthorizeClaude
	default:
		return telegramcontroller.SemanticAction{}, fmt.Errorf("unsupported callback action %q", plan.Action)
	}
	if want := callbackEffectForAction(plan.Action); want == "" || plan.Effect != want {
		return telegramcontroller.SemanticAction{}, errors.New("callback plan action and effect disagree")
	}
	sessionID := plan.SessionID
	if telegramui.IsGlobalAction(plan.Action) {
		if sessionID != domain.SessionID(telegramui.GlobalSurfaceID) {
			return telegramcontroller.SemanticAction{}, errors.New("global callback plan has invalid surface identity")
		}
		sessionID = ""
	}
	return telegramcontroller.SemanticAction{Kind: kind, SessionID: sessionID, Page: plan.Target.Page, FollowLatest: plan.Target.FollowLatest, SessionSlot: plan.Target.SessionSlot, UpdateID: plan.UpdateID}, nil
}

func callbackEffectForAction(action telegramui.Action) telegrampipeline.CallbackEffect {
	switch action {
	case telegramui.ActionPagePrevious, telegramui.ActionPageLatest, telegramui.ActionPageNext:
		return telegrampipeline.EffectProjectPage
	case telegramui.ActionStop:
		return telegrampipeline.EffectStopSession
	case telegramui.ActionClose:
		return telegrampipeline.EffectCloseSession
	case telegramui.ActionOptions:
		return telegrampipeline.EffectToggleOptions
	case telegramui.ActionScreen:
		return telegrampipeline.EffectToggleGlobalScreen
	case telegramui.ActionSelectSession:
		return telegrampipeline.EffectSelectSession
	case telegramui.ActionResume:
		return telegrampipeline.EffectResumeSession
	case telegramui.ActionMenuSessions:
		return telegrampipeline.EffectOpenSessions
	case telegramui.ActionMenuNew:
		return telegrampipeline.EffectOpenNew
	case telegramui.ActionMenuArchive:
		return telegrampipeline.EffectOpenArchive
	case telegramui.ActionMenuStatus:
		return telegrampipeline.EffectShowStatus
	case telegramui.ActionMenuSettings:
		return telegrampipeline.EffectOpenSettings
	case telegramui.ActionMenuBack:
		return telegrampipeline.EffectOpenMenu
	case telegramui.ActionCreateCodex:
		return telegrampipeline.EffectCreateCodex
	case telegramui.ActionCreateClaude:
		return telegrampipeline.EffectCreateClaude
	case telegramui.ActionSettingsScreen:
		return telegrampipeline.EffectToggleSettingsScreen
	case telegramui.ActionSettingsDetail:
		return telegrampipeline.EffectToggleSettingsDetail
	case telegramui.ActionAuthorizeCodex:
		return telegrampipeline.EffectAuthorizeCodex
	case telegramui.ActionAuthorizeClaude:
		return telegrampipeline.EffectAuthorizeClaude
	default:
		return ""
	}
}

func projectSemanticResult(result telegramcontroller.SemanticActionResult, effect telegramui.CarrierEffect) (*telegramflow.CardOutput, *telegramflow.SurfaceOutput, error) {
	if result.Card != nil && result.Surface != nil {
		return nil, nil, errors.New("semantic result contains both card and surface")
	}
	if result.Card != nil {
		card, err := projectSemanticCard(*result.Card, effect)
		return card, nil, err
	}
	if result.Surface != nil {
		surface, err := projectSemanticSurface(*result.Surface)
		return nil, surface, err
	}
	return nil, nil, nil
}

func projectSemanticCard(card telegramcontroller.SemanticCard, effect telegramui.CarrierEffect) (*telegramflow.CardOutput, error) {
	if card.Effect != telegramcontroller.SemanticEditSameCarrier {
		return nil, fmt.Errorf("unsupported semantic carrier effect %q", card.Effect)
	}
	pages := make([]telegramui.ContentPage, len(card.Pages))
	for index, page := range card.Pages {
		pages[index] = telegramui.ContentPage{Content: page.Content, Anchors: append([]string(nil), page.Anchors...)}
	}
	view := telegramui.PageView{Page: card.View.Page, Pages: card.View.Pages, Anchor: card.View.Anchor, FollowLatest: card.View.FollowLatest}
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{View: view, Working: card.Working, Archived: card.Archived, OptionsExpanded: card.OptionsExpanded, SessionRowSizes: append([]int(nil), card.SessionRowSizes...)})
	if err != nil {
		return nil, fmt.Errorf("project semantic card keyboard: %w", err)
	}
	return &telegramflow.CardOutput{SessionID: card.SessionID, Header: card.Header, Projection: telegramui.CarrierProjection{Effect: effect, Card: telegramui.ProjectedCard{Pages: pages, View: view, Keyboard: keyboard}}, OptionsExpanded: card.OptionsExpanded, SelectableSessionIDs: append([]domain.SessionID(nil), card.SelectableSessionIDs...), MakeActive: card.MakeActive}, nil
}

func projectSemanticSurface(surface telegramcontroller.SemanticSurface) (*telegramflow.SurfaceOutput, error) {
	if surface.Text == "" || len(surface.Rows) == 0 {
		return nil, errors.New("semantic surface content and rows are required")
	}
	keyboard := telegramui.CardKeyboard{Rows: make([]telegramui.ButtonRow, len(surface.Rows))}
	selectable := make([]domain.SessionID, 0)
	for rowIndex, row := range surface.Rows {
		if len(row) == 0 {
			return nil, errors.New("semantic surface row is empty")
		}
		keyboard.Rows[rowIndex] = make(telegramui.ButtonRow, len(row))
		for buttonIndex, semantic := range row {
			action, err := telegramUIAction(semantic.Action)
			if err != nil {
				return nil, err
			}
			button := telegramui.Button{Action: action}
			if action == telegramui.ActionSelectSession || action == telegramui.ActionResume {
				if semantic.SessionID == "" {
					return nil, errors.New("selectable semantic surface action requires a session")
				}
				selectable = append(selectable, semantic.SessionID)
				button.Target.SessionSlot = len(selectable)
			} else if semantic.SessionID != "" {
				return nil, errors.New("global semantic surface action has an unexpected session")
			}
			keyboard.Rows[rowIndex][buttonIndex] = button
		}
	}
	return &telegramflow.SurfaceOutput{Text: surface.Text, Keyboard: keyboard, SelectableSessionIDs: selectable}, nil
}

func telegramUIAction(action telegramcontroller.SemanticActionKind) (telegramui.Action, error) {
	switch action {
	case telegramcontroller.SemanticPagePrevious:
		return telegramui.ActionPagePrevious, nil
	case telegramcontroller.SemanticPageLatest:
		return telegramui.ActionPageLatest, nil
	case telegramcontroller.SemanticPageNext:
		return telegramui.ActionPageNext, nil
	case telegramcontroller.SemanticStop:
		return telegramui.ActionStop, nil
	case telegramcontroller.SemanticClose:
		return telegramui.ActionClose, nil
	case telegramcontroller.SemanticOptions:
		return telegramui.ActionOptions, nil
	case telegramcontroller.SemanticScreen:
		return telegramui.ActionScreen, nil
	case telegramcontroller.SemanticSelect:
		return telegramui.ActionSelectSession, nil
	case telegramcontroller.SemanticResume:
		return telegramui.ActionResume, nil
	case telegramcontroller.SemanticMenuSessions:
		return telegramui.ActionMenuSessions, nil
	case telegramcontroller.SemanticMenuNew:
		return telegramui.ActionMenuNew, nil
	case telegramcontroller.SemanticMenuArchive:
		return telegramui.ActionMenuArchive, nil
	case telegramcontroller.SemanticMenuStatus:
		return telegramui.ActionMenuStatus, nil
	case telegramcontroller.SemanticMenuSettings:
		return telegramui.ActionMenuSettings, nil
	case telegramcontroller.SemanticMenuBack:
		return telegramui.ActionMenuBack, nil
	case telegramcontroller.SemanticCreateCodex:
		return telegramui.ActionCreateCodex, nil
	case telegramcontroller.SemanticCreateClaude:
		return telegramui.ActionCreateClaude, nil
	case telegramcontroller.SemanticSettingsScreen:
		return telegramui.ActionSettingsScreen, nil
	case telegramcontroller.SemanticSettingsDetail:
		return telegramui.ActionSettingsDetail, nil
	case telegramcontroller.SemanticAuthorizeCodex:
		return telegramui.ActionAuthorizeCodex, nil
	case telegramcontroller.SemanticAuthorizeClaude:
		return telegramui.ActionAuthorizeClaude, nil
	default:
		return "", fmt.Errorf("unsupported semantic surface action %q", action)
	}
}

type TelegramUIStateStore interface {
	LoadTelegramUI(context.Context) (telegramstate.State, error)
	UpdateTelegramUI(context.Context, func(*telegramstate.State) error) error
}
type SessionTelegramUIStore struct{ State TelegramUIStateStore }

func (store SessionTelegramUIStore) Load(ctx context.Context) (telegramstate.State, error) {
	if store.State == nil {
		return telegramstate.State{}, errors.New("session state is required")
	}
	return store.State.LoadTelegramUI(ctx)
}
func (store SessionTelegramUIStore) Update(ctx context.Context, update func(*telegramstate.State) error) error {
	if store.State == nil {
		return errors.New("session state is required")
	}
	return store.State.UpdateTelegramUI(ctx, update)
}

type PendingStatusDelivery interface {
	DeliverPendingStatuses(context.Context, int) error
}
type EnqueuedOutboundConfirmer interface {
	ConfirmEnqueuedOutbound(context.Context, string, int64) (coordinator.StoredCheckpoint, error)
}
type StatusDeliveryRunner struct {
	Delivery    PendingStatusDelivery
	Checkpoints coordinator.CheckpointStore
	Confirmer   EnqueuedOutboundConfirmer
	Interval    time.Duration
	Limit       int
	Report      func(error)
}
type OutboundReceiptReconciler struct {
	Checkpoints coordinator.CheckpointStore
	Confirmer   EnqueuedOutboundConfirmer
	Interval    time.Duration
	Report      func(error)
}

func (runner OutboundReceiptReconciler) Run(ctx context.Context) error {
	if ctx == nil || runner.Checkpoints == nil || runner.Confirmer == nil || runner.Interval <= 0 || runner.Report == nil {
		return errors.New("outbound receipt reconciliation dependencies are required")
	}
	reconcile := func() {
		if err := ReconcileEnqueuedOutbound(ctx, runner.Checkpoints, runner.Confirmer); err != nil && !errors.Is(err, coordinator.ErrDeliveryUnknown) && !errors.Is(err, context.Canceled) {
			runner.Report(err)
		}
	}
	reconcile()
	ticker := time.NewTicker(runner.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			reconcile()
		}
	}
}

func ReconcileEnqueuedOutbound(ctx context.Context, checkpoints coordinator.CheckpointStore, confirmer EnqueuedOutboundConfirmer) error {
	stored, found, err := checkpoints.Load(ctx)
	if err != nil || !found {
		return err
	}
	if stored.Checkpoint.Outbound == nil || stored.Checkpoint.Outbound.Phase != coordinator.OutboundEnqueued {
		return nil
	}
	outbound := stored.Checkpoint.Outbound
	_, err = confirmer.ConfirmEnqueuedOutbound(ctx, outbound.OperationID, outbound.UpdateID)
	return err
}

func (runner StatusDeliveryRunner) Run(ctx context.Context) error {
	if ctx == nil || runner.Delivery == nil || runner.Checkpoints == nil || runner.Confirmer == nil || runner.Interval <= 0 || runner.Limit <= 0 || runner.Report == nil {
		return errors.New("Telegram status delivery dependencies are required")
	}
	deliver := func() {
		if err := runner.Delivery.DeliverPendingStatuses(ctx, runner.Limit); err != nil && !errors.Is(err, context.Canceled) {
			runner.Report(err)
		}
		if err := ReconcileEnqueuedOutbound(ctx, runner.Checkpoints, runner.Confirmer); err != nil && !errors.Is(err, coordinator.ErrDeliveryUnknown) && !errors.Is(err, context.Canceled) {
			runner.Report(err)
		}
	}
	deliver()
	ticker := time.NewTicker(runner.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			deliver()
		}
	}
}

var _ telegramflow.MessageExecutor = ControllerFlowAdapter{}
var _ telegramflow.CallbackExecutor = ControllerFlowAdapter{}
var _ telegramstate.Store = SessionTelegramUIStore{}
