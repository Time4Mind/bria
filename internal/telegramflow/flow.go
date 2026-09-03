package telegramflow

import (
	"bria/internal/callbacktoken"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramops"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecovery"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"sync"
)

type CallbackExecutor interface {
	HandleCallback(context.Context, telegrampipeline.CallbackPlan) (CallbackResult, error)
}
type CallbackResult struct {
	OperationID string
	Card        *CardOutput
	Surface     *SurfaceOutput
	Terminal    *TerminalOutput
}
type MessageExecutor interface {
	HandleMessage(context.Context, coordinator.Update) (MessageResult, error)
}
type MessageResult struct {
	Decision coordinator.Decision
	Card     *CardOutput
	Surface  *SurfaceOutput
}
type SurfaceOutput struct {
	Text                 string
	Keyboard             telegramui.CardKeyboard
	SelectableSessionIDs []domain.SessionID
	InteractionSessionID domain.SessionID
	InteractionRequestID string
	OutboundOperationID  string
	OutboundUpdateID     int64
	Recovery             *UnknownCallbackOperation
	AcceptedTurnRecovery *telegrampipeline.AcceptedTurnRecoveryBinding
	StatusRecovery       *StatusRecoveryBinding
	ArtifactRetry        *telegrampipeline.ArtifactRetryBinding
}
type TerminalOutput struct {
	Text string
}
type CardOutput struct {
	SessionID            domain.SessionID
	Header               string
	Projection           telegramui.CarrierProjection
	OptionsExpanded      bool
	SelectableSessionIDs []domain.SessionID
	MakeActive           bool
}
type TransportSender interface {
	coordinator.Sender
	coordinator.KeyboardSender
	coordinator.CarrierEditor
}
type Config struct {
	OwnerUserID        int64
	OwnerPrivateChatID int64
	Presenter          *telegrambridge.Presenter
	CallbackRegistry   telegrampipeline.CallbackRegistry
	UIState            telegramstate.Store
	Messages           coordinator.Handler
	MessageUI          MessageExecutor
	Callbacks          CallbackExecutor
	Operations         CallbackOperationStore
	Sender             TransportSender
}
type pendingStore struct {
	mu    sync.Mutex
	items map[string]Prepared
}
type Handler struct {
	ownerUserID        int64
	ownerPrivateChatID int64
	presenter          *telegrambridge.Presenter
	registry           telegrampipeline.CallbackRegistry
	cards              telegrampipeline.StateCardStore
	uiState            telegramstate.Store
	messages           coordinator.Handler
	messageUI          MessageExecutor
	callbacks          CallbackExecutor
	operations         CallbackOperationStore
	pending            *pendingStore
}
type Sender struct {
	base       TransportSender
	registry   telegrampipeline.CallbackRegistry
	uiState    telegramstate.Store
	operations CallbackOperationStore
	pending    *pendingStore
}
type UnknownCallbackOperation struct {
	OwnerUserID        int64
	OwnerPrivateChatID int64
	OperationID        string
	UpdateID           int64
	SessionID          domain.SessionID
	Carrier            telegramstate.Carrier
	Phase              CallbackOperationPhase
	Effect             telegrampipeline.CallbackEffect
}

var (
	_ coordinator.Handler                 = (*Handler)(nil)
	_ coordinator.Sender                  = (*Sender)(nil)
	_ coordinator.KeyboardSender          = (*Sender)(nil)
	_ coordinator.CarrierEditor           = (*Sender)(nil)
	_ coordinator.DurableStatusSender     = (*Sender)(nil)
	_ coordinator.OutboundReceiptResolver = (*Sender)(nil)
)

func New(config Config) (*Handler, *Sender, error) {
	if config.OwnerUserID <= 0 || config.OwnerPrivateChatID <= 0 {
		return nil, nil, errors.New("Telegram owner identity is required")
	}
	if config.Presenter == nil || config.CallbackRegistry == nil || config.UIState == nil ||
		(config.Messages == nil && config.MessageUI == nil) || config.Callbacks == nil || config.Operations == nil || config.Sender == nil {
		return nil, nil, errors.New("Telegram flow dependencies are required")
	}
	cards, err := telegrampipeline.NewStateCardStore(config.UIState)
	if err != nil {
		return nil, nil, err
	}
	pending := &pendingStore{items: make(map[string]Prepared)}
	return &Handler{
			ownerUserID:        config.OwnerUserID,
			ownerPrivateChatID: config.OwnerPrivateChatID,
			presenter:          config.Presenter,
			registry:           config.CallbackRegistry,
			cards:              cards,
			uiState:            config.UIState,
			messages:           config.Messages,
			messageUI:          config.MessageUI,
			callbacks:          config.Callbacks,
			operations:         config.Operations,
			pending:            pending,
		}, &Sender{
			base:       config.Sender,
			registry:   config.CallbackRegistry,
			uiState:    config.UIState,
			operations: config.Operations,
			pending:    pending,
		}, nil
}
func (handler *Handler) ListUnknown(ctx context.Context, limit int) ([]UnknownCallbackOperation, error) {
	if handler == nil || handler.operations == nil {
		return nil, errors.New("Telegram flow handler is required")
	}
	operations, err := handler.operations.ListUnknown(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := make([]UnknownCallbackOperation, len(operations))
	for index, operation := range operations {
		result[index] = UnknownCallbackOperation{
			OwnerUserID: handler.ownerUserID, OwnerPrivateChatID: handler.ownerPrivateChatID,
			OperationID: operation.ID, UpdateID: operation.UpdateID, SessionID: operation.Plan.SessionID,
			Carrier: operation.Plan.Carrier, Phase: operation.Phase, Effect: operation.Plan.Effect,
		}
	}
	return result, nil
}
func (handler *Handler) PrepareUnknownRecovery(ctx context.Context, update coordinator.Update) (coordinator.RecoveryControl, coordinator.Decision, error) {
	if handler == nil || handler.operations == nil || handler.presenter == nil {
		return coordinator.RecoveryControl{}, coordinator.Decision{}, errors.New("Telegram recovery dependencies are required")
	}
	if update.ID <= 0 || update.Kind != coordinator.UpdateCallback || update.ActorID != handler.ownerUserID ||
		update.ConversationID != handler.ownerPrivateChatID || update.ConversationKind != "private" ||
		update.CallbackQueryID == "" || update.SourceMessageID <= 0 {
		return coordinator.RecoveryControl{}, coordinator.Decision{}, errors.New("unknown callback recovery identity is invalid")
	}
	operationID := "status:" + strconv.FormatInt(update.ID, 10)
	operation, found, err := handler.operations.Load(ctx, operationID)
	if err != nil {
		return coordinator.RecoveryControl{}, coordinator.Decision{}, fmt.Errorf("load unknown callback operation: %w", err)
	}
	if !found || (operation.Phase != CallbackEffectUnknown && operation.Phase != CallbackEffectRetryUnknown && operation.Phase != CallbackSendUnknown) ||
		operation.UpdateID != update.ID || operation.CallbackQueryID != update.CallbackQueryID ||
		operation.CallbackDigest != fmt.Sprintf("%x", sha256.Sum256([]byte(update.Text))) ||
		operation.Plan.Carrier != (telegramstate.Carrier{ChatID: update.ConversationID, MessageID: update.SourceMessageID}) {
		return coordinator.RecoveryControl{}, coordinator.Decision{}, errors.New("unknown callback recovery no longer matches its exact update")
	}
	unknown := UnknownCallbackOperation{
		OwnerUserID: handler.ownerUserID, OwnerPrivateChatID: handler.ownerPrivateChatID,
		OperationID: operation.ID, UpdateID: operation.UpdateID, SessionID: operation.Plan.SessionID,
		Carrier: operation.Plan.Carrier, Phase: operation.Phase, Effect: operation.Plan.Effect,
	}
	promptOperationID := "recovery:callback:" + strconv.FormatInt(update.ID, 10)
	text, keyboard, err := telegramrecovery.ProjectUnknown(string(unknown.Phase), string(unknown.SessionID), unknown.OperationID)
	if err == nil {
		copyUnknown := unknown
		prepared, err := PrepareSurface(promptOperationID, handler.ownerPrivateChatID, "", 0, false,
			SurfaceOutput{Text: text, Keyboard: keyboard, Recovery: &copyUnknown}, handler.presenter)
		if err == nil {
			if err = handler.pending.register(prepared); err == nil {
				return coordinator.RecoveryControl{OriginalOperationID: operation.ID, PromptOperationID: promptOperationID, UpdateID: update.ID}, decisionFromPrepared(prepared), nil
			}
		}
	}
	if err != nil {
		return coordinator.RecoveryControl{}, coordinator.Decision{}, fmt.Errorf("project unknown callback recovery: %w", err)
	}
	return coordinator.RecoveryControl{}, coordinator.Decision{}, errors.New("project unknown callback recovery")
}
func (handler *Handler) Handle(ctx context.Context, update coordinator.Update) (coordinator.Decision, error) {
	if update.Kind != coordinator.UpdateCallback {
		if handler.messageUI != nil {
			result, err := handler.messageUI.HandleMessage(ctx, update)
			if err != nil {
				return coordinator.Decision{}, err
			}
			return handler.prepareMessageResult(update, result)
		}
		decision, err := handler.messages.Handle(ctx, update)
		if err == nil && decision.Keyboard != nil {
			return coordinator.Decision{}, errors.New("unsigned Telegram keyboard rejected")
		}
		return decision, err
	}
	if update.ActorID != handler.ownerUserID || update.ConversationID != handler.ownerPrivateChatID ||
		update.ConversationKind != "private" {
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
	operationID := "status:" + strconv.FormatInt(update.ID, 10)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(update.Text)))
	if existing, found, err := handler.operations.Load(ctx, operationID); err != nil {
		return coordinator.Decision{}, fmt.Errorf("load callback operation: %w", err)
	} else if found {
		return handler.resumeOperation(ctx, update, digest, existing)
	}
	accepted, err := telegrampipeline.AcceptCallbackForDurableOperation(
		ctx,
		update,
		handler.ownerUserID,
		handler.ownerPrivateChatID,
		handler.cards,
		handler.registry,
		handler.presenter,
	)
	if err != nil {
		if recoverableCallbackError(err) {
			return coordinator.Decision{
				Kind: coordinator.DecisionStatus,
				Status: coordinator.Status{
					ConversationID:  update.ConversationID,
					Text:            "Кнопка недействительна или устарела. Откройте актуальную карточку.",
					CallbackQueryID: update.CallbackQueryID,
				},
			}, nil
		}
		return coordinator.Decision{}, err
	}
	plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
	if err != nil {
		return coordinator.Decision{}, err
	}
	operation := CallbackOperation{
		ID: operationID, UpdateID: update.ID, CallbackQueryID: update.CallbackQueryID,
		CallbackDigest: digest, Plan: plan, Phase: CallbackClaimed,
	}
	if err := handler.operations.Create(ctx, operation); err != nil {
		if !errors.Is(err, ErrCallbackOperationExists) {
			return coordinator.Decision{}, fmt.Errorf("persist claimed callback: %w", err)
		}
		existing, found, loadErr := handler.operations.Load(ctx, operationID)
		if loadErr != nil || !found {
			return coordinator.Decision{}, fmt.Errorf("reload concurrent callback operation: %w", loadErr)
		}
		return handler.resumeOperation(ctx, update, digest, existing)
	}
	return handler.executeClaimed(ctx, update, operation)
}
func (handler *Handler) executeClaimed(ctx context.Context, update coordinator.Update, operation CallbackOperation) (coordinator.Decision, error) {
	effectUnknown := operation
	effectUnknown.Phase = CallbackEffectUnknown
	changed, err := handler.operations.CompareAndSwap(ctx, operation.ID, CallbackClaimed, effectUnknown)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("fence callback effect: %w", err)
	}
	if !changed {
		existing, found, loadErr := handler.operations.Load(ctx, operation.ID)
		if loadErr != nil || !found {
			return coordinator.Decision{}, fmt.Errorf("reload fenced callback operation: %w", loadErr)
		}
		return handler.resumeOperation(ctx, update, operation.CallbackDigest, existing)
	}
	result, err := handler.callbacks.HandleCallback(ctx, operation.Plan)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("%w: callback effect: %v", telegrampipeline.ErrUnknownOperation, err)
	}
	if callbackResultCount(result) != 1 {
		return coordinator.Decision{}, fmt.Errorf("%w: callback result must contain exactly one card, surface, or terminal output", telegrampipeline.ErrUnknownOperation)
	}
	if result.OperationID != operation.ID {
		return coordinator.Decision{}, fmt.Errorf("%w: callback executor did not acknowledge operation %s", telegrampipeline.ErrUnknownOperation, operation.ID)
	}
	prepared, err := handler.prepareCallbackResult(operation, result)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("%w: callback effect output: %v", telegrampipeline.ErrUnknownOperation, err)
	}
	preparedOperation := effectUnknown
	preparedOperation.Phase = CallbackPrepared
	preparedOperation.Prepared = &prepared
	changed, err = handler.operations.CompareAndSwap(ctx, operation.ID, CallbackEffectUnknown, preparedOperation)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist callback effect output: %w", err)
	}
	if !changed {
		return coordinator.Decision{}, fmt.Errorf("%w: %s", telegrampipeline.ErrUnknownOperation, operation.ID)
	}
	if err := handler.pending.register(prepared); err != nil {
		return coordinator.Decision{}, err
	}
	return coordinator.Decision{
		Kind:     coordinator.DecisionStatus,
		Status:   prepared.Status,
		Keyboard: prepared.Keyboard,
	}, nil
}
func (handler *Handler) prepareCallbackResult(operation CallbackOperation, result CallbackResult) (Prepared, error) {
	if result.OperationID != operation.ID || callbackResultCount(result) != 1 {
		return Prepared{}, errors.New("callback result acknowledgement or output is invalid")
	}
	if result.Card != nil {
		return prepareCard(operation.ID, operation.Plan.Carrier.ChatID, operation.CallbackQueryID, operation.Plan.Carrier.MessageID, *result.Card, handler.presenter)
	}
	if result.Surface != nil {
		return PrepareSurface(operation.ID, operation.Plan.Carrier.ChatID, operation.CallbackQueryID, operation.Plan.Carrier.MessageID, true, *result.Surface, handler.presenter)
	}
	return prepareTerminal(operation.ID, operation.Plan.Carrier.ChatID, operation.CallbackQueryID, operation.Plan.Carrier.MessageID, *result.Terminal)
}
func callbackResultCount(result CallbackResult) int {
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
func (handler *Handler) resumeOperation(ctx context.Context, update coordinator.Update, digest string, operation CallbackOperation) (coordinator.Decision, error) {
	if operation.UpdateID != update.ID || operation.CallbackQueryID != update.CallbackQueryID || operation.CallbackDigest != digest ||
		operation.Plan.Carrier != (telegramstate.Carrier{ChatID: update.ConversationID, MessageID: update.SourceMessageID}) {
		return coordinator.Decision{}, errors.New("callback operation identity collision")
	}
	switch operation.Phase {
	case CallbackClaimed:
		return handler.executeClaimed(ctx, update, operation)
	case CallbackPrepared:
		if operation.Prepared == nil {
			return coordinator.Decision{}, errors.New("prepared callback operation has no output")
		}
		if !operation.Prepared.Terminal && !presentationCurrentlyValid(handler.presenter, operation.Prepared.Presentation) {
			var refreshed Prepared
			var err error
			if operation.Prepared.Surface != nil {
				refreshed, err = PrepareSurface(operation.ID, operation.Prepared.Status.ConversationID, operation.Prepared.Status.CallbackQueryID, operation.Prepared.Status.SourceMessageID, operation.Prepared.Edit, *operation.Prepared.Surface, handler.presenter)
			} else {
				refreshed, err = prepareCard(operation.ID, operation.Prepared.Status.ConversationID, operation.Prepared.Status.CallbackQueryID, operation.Prepared.Status.SourceMessageID, operation.Prepared.Card, handler.presenter)
			}
			if err != nil {
				return coordinator.Decision{}, fmt.Errorf("refresh expired callback presentation: %w", err)
			}
			refreshedOperation := operation
			refreshedOperation.Prepared = &refreshed
			changed, err := handler.operations.CompareAndSwap(ctx, operation.ID, CallbackPrepared, refreshedOperation)
			if err != nil {
				return coordinator.Decision{}, fmt.Errorf("persist refreshed callback presentation: %w", err)
			}
			if !changed {
				return coordinator.Decision{}, errors.New("callback presentation changed while refreshing")
			}
			handler.pending.remove(operation.ID)
			operation = refreshedOperation
		}
		if err := handler.pending.register(*operation.Prepared); err != nil {
			return coordinator.Decision{}, err
		}
		return decisionFromPrepared(*operation.Prepared), nil
	case CallbackReceiptConfirmed:
		if operation.Prepared == nil || operation.Receipt <= 0 {
			return coordinator.Decision{}, errors.New("confirmed callback operation has no receipt")
		}
		if err := finalizePrepared(ctx, handler.registry, handler.uiState, *operation.Prepared, operation.Receipt); err != nil {
			return coordinator.Decision{}, err
		}
		committed := operation
		committed.Phase = CallbackCommitted
		changed, err := handler.operations.CompareAndSwap(ctx, operation.ID, CallbackReceiptConfirmed, committed)
		if err != nil {
			return coordinator.Decision{}, fmt.Errorf("commit recovered callback operation: %w", err)
		}
		if !changed {
			return coordinator.Decision{}, errors.New("recovered callback operation phase changed")
		}
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	case CallbackEffectResolved, CallbackCommitted:
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	case CallbackEffectUnknown, CallbackEffectRetryUnknown, CallbackSendUnknown:
		return coordinator.Decision{}, fmt.Errorf("%w: %s is %s", telegrampipeline.ErrUnknownOperation, operation.ID, operation.Phase)
	default:
		return coordinator.Decision{}, errors.New("callback operation has unsupported phase")
	}
}
func (handler *Handler) prepareMessageResult(update coordinator.Update, result MessageResult) (coordinator.Decision, error) {
	if result.Card == nil && result.Surface == nil {
		if result.Decision.Keyboard != nil {
			return coordinator.Decision{}, errors.New("unsigned Telegram keyboard rejected")
		}
		return result.Decision, nil
	}
	if result.Card != nil && result.Surface != nil {
		return coordinator.Decision{}, errors.New("message UI result must contain at most one card or surface")
	}
	operationID := "status:" + strconv.FormatInt(update.ID, 10)
	var prepared Prepared
	var err error
	if result.Card != nil {
		prepared, err = prepareCard(operationID, update.ConversationID, "", 0, *result.Card, handler.presenter)
	} else {
		prepared, err = PrepareSurface(operationID, update.ConversationID, "", 0, false, *result.Surface, handler.presenter)
	}
	if err != nil {
		return coordinator.Decision{}, err
	}
	if err := handler.pending.register(prepared); err != nil {
		return coordinator.Decision{}, err
	}
	return decisionFromPrepared(prepared), nil
}
func presentationCurrentlyValid(presenter *telegrambridge.Presenter, presentation telegrambridge.KeyboardPresentation) bool {
	if presenter == nil || len(presentation.TokenIDs) == 0 {
		return false
	}
	index := 0
	for _, row := range presentation.Markup.InlineKeyboard {
		for _, button := range row {
			if index >= len(presentation.TokenIDs) {
				return false
			}
			decoded, err := presenter.DecodeCallbackWithMetadata(button.CallbackData)
			if err != nil || decoded.TokenID != presentation.TokenIDs[index] || decoded.ExpiresAt != presentation.ExpiresAt {
				return false
			}
			index++
		}
	}
	return index == len(presentation.TokenIDs)
}
func decisionFromPrepared(prepared Prepared) coordinator.Decision {
	return coordinator.Decision{Kind: coordinator.DecisionStatus, Status: prepared.Status, Keyboard: prepared.Keyboard}
}
func recoverableCallbackError(err error) bool {
	return errors.Is(err, callbacktoken.ErrInvalidToken) ||
		errors.Is(err, callbacktoken.ErrUnsupportedVersion) ||
		errors.Is(err, callbacktoken.ErrUnknownAction) ||
		errors.Is(err, callbacktoken.ErrExpired) ||
		errors.Is(err, callbacktoken.ErrInvalidFields) ||
		errors.Is(err, telegrampipeline.ErrStaleCallback) ||
		errors.Is(err, telegrampipeline.ErrReplayedCallback)
}

type Prepared struct {
	OperationID  string
	Status       coordinator.Status
	Keyboard     *coordinator.KeyboardMarkup
	Presentation telegrambridge.KeyboardPresentation
	Card         CardOutput
	Surface      *SurfaceOutput
	Edit         bool
	Terminal     bool
}

func PrepareCompletion(
	operationID string,
	sessionID domain.SessionID,
	conversationID int64,
	active bool,
	input telegramui.CardProjectionInput,
	optionsExpanded bool,
	selectableSessionIDs []domain.SessionID,
	presenter *telegrambridge.Presenter,
) (Prepared, error) {
	if operationID == "" || sessionID == "" || conversationID <= 0 || presenter == nil {
		return Prepared{}, errors.New("completion identity and presenter are required")
	}
	projection, err := telegramui.ProjectCompletion(input, active)
	if err != nil {
		return Prepared{}, fmt.Errorf("project Telegram completion: %w", err)
	}
	card := CardOutput{
		SessionID:            sessionID,
		Projection:           projection,
		OptionsExpanded:      optionsExpanded,
		SelectableSessionIDs: append([]domain.SessionID(nil), selectableSessionIDs...),
		MakeActive:           active,
	}
	if !active {
		notification, err := presenter.PresentBackgroundCompletion(string(sessionID))
		if err != nil {
			return Prepared{}, err
		}
		return Prepared{
			OperationID: operationID,
			Status: coordinator.Status{
				ConversationID: conversationID,
				Text:           notification.Text,
			},
			Keyboard:     coordinatorKeyboard(notification.Markup),
			Presentation: notification.KeyboardPresentation,
			Card:         card,
		}, nil
	}
	return prepareCard(operationID, conversationID, "", 0, card, presenter)
}
func PrepareInteraction(
	operationID string,
	sessionID domain.SessionID,
	conversationID int64,
	requestID string,
	surface SurfaceOutput,
	presenter *telegrambridge.Presenter,
) (Prepared, error) {
	if sessionID == "" || requestID == "" {
		return Prepared{}, errors.New("interaction session and request identity are required")
	}
	if surface.InteractionSessionID != "" || surface.InteractionRequestID != "" || len(surface.SelectableSessionIDs) != 0 ||
		surface.OutboundOperationID != "" || surface.OutboundUpdateID != 0 || surface.Recovery != nil {
		return Prepared{}, errors.New("interaction surface binding is assigned by PrepareInteraction")
	}
	surface.InteractionSessionID = sessionID
	surface.InteractionRequestID = requestID
	return PrepareSurface(operationID, conversationID, "", 0, false, surface, presenter)
}
func PrepareAcceptedTurnRecovery(
	presentationOperationID string,
	conversationID int64,
	binding telegrampipeline.AcceptedTurnRecoveryBinding,
	presenter *telegrambridge.Presenter,
) (Prepared, error) {
	text, keyboard := telegramrecovery.ProjectAcceptedTurn()
	copyBinding := binding
	return PrepareSurface(presentationOperationID, conversationID, "", 0, false, SurfaceOutput{
		Text: text, Keyboard: keyboard, AcceptedTurnRecovery: &copyBinding,
	}, presenter)
}
func PrepareOutboundResolution(
	presentationOperationID string,
	conversationID int64,
	outboundOperationID string,
	outboundUpdateID int64,
	surface SurfaceOutput,
	presenter *telegrambridge.Presenter,
) (Prepared, error) {
	if outboundOperationID == "" || outboundUpdateID <= 0 {
		return Prepared{}, errors.New("outbound resolution target is required")
	}
	if surface.InteractionSessionID != "" || surface.InteractionRequestID != "" || len(surface.SelectableSessionIDs) != 0 ||
		surface.OutboundOperationID != "" || surface.OutboundUpdateID != 0 || surface.Recovery != nil {
		return Prepared{}, errors.New("outbound resolution surface binding is assigned by PrepareOutboundResolution")
	}
	surface.OutboundOperationID = outboundOperationID
	surface.OutboundUpdateID = outboundUpdateID
	return PrepareSurface(presentationOperationID, conversationID, "", 0, false, surface, presenter)
}
func prepareCard(
	operationID string,
	conversationID int64,
	callbackQueryID string,
	sourceMessageID int64,
	card CardOutput,
	presenter *telegrambridge.Presenter,
) (Prepared, error) {
	if operationID == "" || conversationID <= 0 || card.SessionID == "" || presenter == nil {
		return Prepared{}, errors.New("card operation identity is required")
	}
	projection := card.Projection
	if projection.Notification != nil ||
		(projection.Effect != telegramui.EffectEditSameCarrier && projection.Effect != telegramui.EffectSendOneNewCard) {
		return Prepared{}, errors.New("card projection carrier effect is invalid")
	}
	if projection.Card.View.Page < 1 || projection.Card.View.Page > len(projection.Card.Pages) {
		return Prepared{}, errors.New("projected card page is invalid")
	}
	selectable := make([]string, len(card.SelectableSessionIDs))
	for index, id := range card.SelectableSessionIDs {
		selectable[index] = string(id)
	}
	presentation, err := presenter.PresentKeyboardWithManifest(string(card.SessionID), selectable, projection.Card.Keyboard)
	if err != nil {
		return Prepared{}, err
	}
	if projection.Effect == telegramui.EffectEditSameCarrier {
		if sourceMessageID <= 0 {
			return Prepared{}, errors.New("card edit requires a source carrier")
		}
	} else {
		sourceMessageID = 0
	}
	return Prepared{
		OperationID: operationID,
		Status: coordinator.Status{
			ConversationID:  conversationID,
			Text:            card.Header + projection.Card.Pages[projection.Card.View.Page-1].Content,
			CallbackQueryID: callbackQueryID,
			SourceMessageID: sourceMessageID,
		},
		Keyboard:     coordinatorKeyboard(presentation.Markup),
		Presentation: presentation,
		Card:         cloneCardOutput(card),
		Edit:         projection.Effect == telegramui.EffectEditSameCarrier,
	}, nil
}
func PrepareSurface(
	operationID string,
	conversationID int64,
	callbackQueryID string,
	sourceMessageID int64,
	edit bool,
	surface SurfaceOutput,
	presenter *telegrambridge.Presenter,
) (Prepared, error) {
	if operationID == "" || conversationID <= 0 || surface.Text == "" || presenter == nil || len(surface.Keyboard.Rows) == 0 {
		return Prepared{}, errors.New("surface operation identity and semantic output are required")
	}
	if edit && sourceMessageID <= 0 {
		return Prepared{}, errors.New("surface edit requires a source carrier")
	}
	if !edit {
		sourceMessageID = 0
		callbackQueryID = ""
	}
	selectable := make([]string, len(surface.SelectableSessionIDs))
	for index, id := range surface.SelectableSessionIDs {
		selectable[index] = string(id)
	}
	interaction := surface.InteractionSessionID != "" || surface.InteractionRequestID != ""
	outboundResolution := surface.OutboundOperationID != "" || surface.OutboundUpdateID != 0
	recovery := surface.Recovery != nil
	acceptedTurnRecovery := surface.AcceptedTurnRecovery != nil
	statusRecovery := surface.StatusRecovery != nil
	artifactRetry := surface.ArtifactRetry != nil
	if telegramrecovery.MultipleBindings(interaction, outboundResolution, recovery, acceptedTurnRecovery, statusRecovery, artifactRetry) {
		return Prepared{}, errors.New("surface cannot contain multiple special bindings")
	}
	if interaction && (surface.InteractionSessionID == "" || surface.InteractionRequestID == "" || len(selectable) != 0) {
		return Prepared{}, errors.New("interaction surface binding is incomplete")
	}
	var presentation telegrambridge.KeyboardPresentation
	var err error
	if interaction {
		presentation, err = presenter.PresentInteractionKeyboardWithManifest(string(surface.InteractionSessionID), surface.InteractionRequestID, surface.Keyboard)
	} else if outboundResolution {
		if surface.OutboundOperationID == "" || surface.OutboundUpdateID <= 0 || len(selectable) != 0 {
			return Prepared{}, errors.New("outbound resolution surface binding is incomplete")
		}
		presentation, err = presenter.PresentOutboundResolutionKeyboardWithManifest(surface.OutboundOperationID, surface.OutboundUpdateID, surface.Keyboard)
	} else if recovery {
		binding := telegrambridge.CallbackRecoveryBinding{
			OperationID: surface.Recovery.OperationID, UpdateID: surface.Recovery.UpdateID,
			SessionID: string(surface.Recovery.SessionID), ChatID: surface.Recovery.Carrier.ChatID,
			MessageID: surface.Recovery.Carrier.MessageID, Phase: string(surface.Recovery.Phase),
		}
		presentation, err = presenter.PresentCallbackRecoveryKeyboardWithManifest(binding, surface.Keyboard)
	} else if acceptedTurnRecovery {
		binding := telegrambridge.AcceptedTurnRecoveryBinding{
			SessionID: surface.AcceptedTurnRecovery.SessionID, MessageID: surface.AcceptedTurnRecovery.MessageID,
			BindingGeneration: surface.AcceptedTurnRecovery.BindingGeneration,
		}
		presentation, err = presenter.PresentAcceptedTurnRecoveryKeyboardWithManifest(binding, surface.Keyboard)
	} else if statusRecovery {
		presentation, err = presenter.PresentStatusRecoveryKeyboardWithManifest(*surface.StatusRecovery, surface.Keyboard)
	} else if artifactRetry {
		presentation, err = presenter.PresentArtifactRetryKeyboardWithManifest(*surface.ArtifactRetry, surface.Keyboard)
	} else {
		presentation, err = presenter.PresentKeyboardWithManifest(telegramui.GlobalSurfaceID, selectable, surface.Keyboard)
	}
	if err != nil {
		return Prepared{}, err
	}
	copySurface := cloneSurfaceOutput(surface)
	return Prepared{OperationID: operationID, Status: coordinator.Status{ConversationID: conversationID, Text: surface.Text, CallbackQueryID: callbackQueryID, SourceMessageID: sourceMessageID}, Keyboard: coordinatorKeyboard(presentation.Markup), Presentation: presentation, Surface: &copySurface, Edit: edit}, nil
}
func prepareTerminal(
	operationID string,
	conversationID int64,
	callbackQueryID string,
	sourceMessageID int64,
	terminal TerminalOutput,
) (Prepared, error) {
	if operationID == "" || conversationID <= 0 || sourceMessageID <= 0 || terminal.Text == "" {
		return Prepared{}, errors.New("terminal interaction operation identity and text are required")
	}
	empty := coordinator.KeyboardMarkup{}
	return Prepared{OperationID: operationID, Status: coordinator.Status{ConversationID: conversationID, Text: terminal.Text, CallbackQueryID: callbackQueryID, SourceMessageID: sourceMessageID}, Keyboard: &empty, Edit: true, Terminal: true}, nil
}
func (sender *Sender) Register(prepared Prepared) error {
	if sender == nil || sender.pending == nil {
		return errors.New("Telegram flow sender is required")
	}
	return sender.pending.register(prepared)
}
func (sender *Sender) EnqueueStatus(ctx context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.DurableOutboundReceipt, error) {
	return sender.enqueueStatus(ctx, operationID, status, keyboard, nil)
}
func (sender *Sender) EnqueuePrepared(ctx context.Context, sequence uint64, prepared Prepared) (coordinator.DurableOutboundReceipt, error) {
	if sender == nil || sequence == 0 || prepared.OperationID == "" {
		return coordinator.DurableOutboundReceipt{}, errors.New("durable prepared Telegram status is invalid")
	}
	if existing, found, err := sender.operations.LoadStatus(ctx, prepared.OperationID); err != nil {
		return coordinator.DurableOutboundReceipt{}, err
	} else if found {
		if existing.Sequence != sequence || existing.Prepared == nil || !sameArtifactRetryPrepared(*existing.Prepared, prepared) {
			return coordinator.DurableOutboundReceipt{}, errors.New("durable prepared Telegram status identity collision")
		}
		if existing.Phase == StatusQueued {
			_, _ = sender.deliverStatusOperation(ctx, existing)
		}
		return coordinator.DurableOutboundReceipt{OperationID: existing.ID, Sequence: existing.Sequence}, nil
	}
	if err := sender.Register(prepared); err != nil {
		return coordinator.DurableOutboundReceipt{}, err
	}
	return sender.enqueueStatusSequence(ctx, sequence, prepared.OperationID, prepared.Status, prepared.Keyboard, nil)
}
func sameArtifactRetryPrepared(left, right Prepared) bool {
	return left.OperationID == right.OperationID && left.Status == right.Status && left.Edit == right.Edit && left.Surface != nil && right.Surface != nil &&
		left.Surface.ArtifactRetry != nil && right.Surface.ArtifactRetry != nil && *left.Surface.ArtifactRetry == *right.Surface.ArtifactRetry &&
		left.Surface.Text == right.Surface.Text && reflect.DeepEqual(left.Surface.Keyboard, right.Surface.Keyboard)
}
func (sender *Sender) EnqueueRecoveryStatus(ctx context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup, recovery StatusRecoveryBinding) (coordinator.DurableOutboundReceipt, error) {
	return sender.enqueueStatus(ctx, operationID, status, keyboard, &recovery)
}
func (sender *Sender) enqueueStatus(ctx context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup, recovery *StatusRecoveryBinding) (coordinator.DurableOutboundReceipt, error) {
	if sender == nil || sender.operations == nil || operationID == "" {
		return coordinator.DurableOutboundReceipt{}, errors.New("durable Telegram status sender is required")
	}
	sequence, err := telegramops.StatusSequence(operationID)
	if err != nil {
		return coordinator.DurableOutboundReceipt{}, err
	}
	return sender.enqueueStatusSequence(ctx, sequence, operationID, status, keyboard, recovery)
}
func (sender *Sender) enqueueStatusSequence(ctx context.Context, sequence uint64, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup, recovery *StatusRecoveryBinding) (coordinator.DurableOutboundReceipt, error) {
	if existing, found, loadErr := sender.operations.LoadStatus(ctx, operationID); loadErr != nil {
		return coordinator.DurableOutboundReceipt{}, loadErr
	} else if found {
		if existing.Sequence != sequence || !reflect.DeepEqual(existing.Status, status) || !reflect.DeepEqual(existing.Keyboard, keyboard) || !reflect.DeepEqual(existing.Recovery, recovery) {
			return coordinator.DurableOutboundReceipt{}, errors.New("durable status operation identity collision")
		}
		if existing.Phase == StatusQueued {
			_, _ = sender.deliverStatusOperation(ctx, existing)
		}
		return coordinator.DurableOutboundReceipt{OperationID: existing.ID, Sequence: existing.Sequence}, nil
	}
	var prepared *Prepared
	if candidate, found := sender.pending.peek(operationID); found {
		if !reflect.DeepEqual(candidate.Status, status) || !reflect.DeepEqual(candidate.Keyboard, keyboard) {
			return coordinator.DurableOutboundReceipt{}, errors.New("prepared Telegram operation does not match durable enqueue")
		}
		copyPrepared := clonePrepared(candidate)
		prepared = &copyPrepared
	}
	operation := StatusOperation{
		ID: operationID, Sequence: sequence, Status: status, Keyboard: cloneCoordinatorKeyboard(keyboard),
		Prepared: prepared, Edit: status.SourceMessageID > 0, Phase: StatusQueued, Recovery: recovery,
	}
	persisted, _, err := sender.operations.EnqueueStatus(ctx, operation)
	if err != nil {
		return coordinator.DurableOutboundReceipt{}, fmt.Errorf("enqueue durable Telegram status: %w", err)
	}
	if persisted.Phase == StatusQueued {
		_, _ = sender.deliverStatusOperation(ctx, persisted)
	}
	return coordinator.DurableOutboundReceipt{OperationID: persisted.ID, Sequence: persisted.Sequence}, nil
}
func (sender *Sender) DeliverPendingStatuses(ctx context.Context, limit int) error {
	operations, err := sender.operations.ListQueuedStatuses(ctx, limit)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if _, err := sender.deliverStatusOperation(ctx, operation); err != nil {
			return err
		}
	}
	return nil
}
func (sender *Sender) deliverStatusOperation(ctx context.Context, operation StatusOperation) (coordinator.Receipt, error) {
	unknown := operation
	unknown.Phase = StatusSendUnknown
	changed, err := sender.operations.CompareAndSwapStatus(ctx, operation.ID, StatusQueued, unknown)
	if err != nil || !changed {
		if err != nil {
			return coordinator.Receipt{}, err
		}
		return coordinator.Receipt{}, errors.New("durable status phase changed before send")
	}
	if operation.Prepared != nil {
		if err := sender.pending.register(*operation.Prepared); err != nil {
			return coordinator.Receipt{}, err
		}
	}
	var receipt coordinator.Receipt
	if operation.Prepared != nil {
		if operation.Edit {
			receipt, err = sender.sendPrepared(ctx, operation.ID, operation.Status, operation.Keyboard, true)
		} else {
			receipt, err = sender.sendPrepared(ctx, operation.ID, operation.Status, operation.Keyboard, false)
		}
	} else if operation.Edit {
		receipt, err = sender.base.EditStatusWithKeyboard(ctx, operation.ID, operation.Status, operation.Keyboard)
	} else if operation.Keyboard != nil {
		receipt, err = sender.base.SendStatusWithKeyboard(ctx, operation.ID, operation.Status, operation.Keyboard)
	} else {
		receipt, err = sender.base.SendStatus(ctx, operation.ID, operation.Status)
	}
	if err != nil || receipt.MessageID <= 0 {
		if err != nil {
			return coordinator.Receipt{}, err
		}
		return coordinator.Receipt{}, errors.New("Telegram carrier returned no positive receipt")
	}
	confirmed := unknown
	confirmed.Phase = StatusReceiptConfirmed
	confirmed.Receipt = receipt.MessageID
	changed, err = sender.operations.CompareAndSwapStatus(context.WithoutCancel(ctx), operation.ID, StatusSendUnknown, confirmed)
	if err != nil || !changed {
		if err != nil {
			return coordinator.Receipt{}, err
		}
		return coordinator.Receipt{}, errors.New("durable status receipt phase changed")
	}
	committed := confirmed
	committed.Phase = StatusCommitted
	changed, err = sender.operations.CompareAndSwapStatus(context.WithoutCancel(ctx), operation.ID, StatusReceiptConfirmed, committed)
	if err != nil || !changed {
		if err != nil {
			return coordinator.Receipt{}, err
		}
		return coordinator.Receipt{}, errors.New("durable status commit phase changed")
	}
	return receipt, nil
}
func (sender *Sender) ResolveStatusReceipt(ctx context.Context, operationID string) (coordinator.Receipt, bool, error) {
	operation, found, err := sender.operations.LoadStatus(ctx, operationID)
	if err != nil || !found {
		return coordinator.Receipt{}, false, err
	}
	if operation.Phase != StatusReceiptConfirmed && operation.Phase != StatusCommitted {
		return coordinator.Receipt{}, false, nil
	}
	if operation.Receipt <= 0 {
		return coordinator.Receipt{}, false, errors.New("durable status has invalid receipt")
	}
	return coordinator.Receipt{MessageID: operation.Receipt}, true, nil
}
func (sender *Sender) RetryUnknownStatus(ctx context.Context, operationID string) error {
	operation, found, err := sender.operations.LoadStatus(ctx, operationID)
	if err != nil {
		return err
	}
	if !found || operation.Phase != StatusSendUnknown {
		return errors.New("durable status is not awaiting explicit retry")
	}
	next := operation
	next.Phase = StatusQueued
	changed, err := sender.operations.CompareAndSwapStatus(ctx, operationID, StatusSendUnknown, next)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("durable status changed while authorizing retry")
	}
	return nil
}
func (sender *Sender) ListUnknownStatuses(ctx context.Context, limit int) ([]StatusOperation, error) {
	if sender == nil || sender.operations == nil {
		return nil, errors.New("durable Telegram status sender is required")
	}
	return sender.operations.ListUnknownStatuses(ctx, limit)
}
func (sender *Sender) ConfirmUnknownStatus(ctx context.Context, operationID string, receipt coordinator.Receipt) error {
	if receipt.MessageID <= 0 {
		return errors.New("verified Telegram receipt must be positive")
	}
	operation, found, err := sender.operations.LoadStatus(ctx, operationID)
	if err != nil {
		return err
	}
	if !found || operation.Phase != StatusSendUnknown {
		return errors.New("durable status is not awaiting explicit confirmation")
	}
	if operation.Edit && receipt.MessageID != operation.Status.SourceMessageID {
		return errors.New("verified Telegram edit receipt does not match its source carrier")
	}
	confirmed := operation
	confirmed.Phase = StatusReceiptConfirmed
	confirmed.Receipt = receipt.MessageID
	changed, err := sender.operations.CompareAndSwapStatus(ctx, operationID, StatusSendUnknown, confirmed)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("durable status changed while confirming receipt")
	}
	if operation.Prepared != nil {
		if callback, exists, loadErr := sender.operations.Load(ctx, operationID); loadErr != nil {
			return loadErr
		} else if exists && callback.Phase == CallbackSendUnknown {
			if err := sender.ConfirmUnknownSend(ctx, operationID, receipt); err != nil {
				return err
			}
		} else if err := finalizePrepared(ctx, sender.registry, sender.uiState, *operation.Prepared, receipt.MessageID); err != nil {
			return err
		}
	}
	committed := confirmed
	committed.Phase = StatusCommitted
	changed, err = sender.operations.CompareAndSwapStatus(ctx, operationID, StatusReceiptConfirmed, committed)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("durable status changed while committing receipt")
	}
	return nil
}
func cloneCoordinatorKeyboard(keyboard *coordinator.KeyboardMarkup) *coordinator.KeyboardMarkup {
	if keyboard == nil {
		return nil
	}
	clone := make(coordinator.KeyboardMarkup, len(*keyboard))
	for index, row := range *keyboard {
		clone[index] = append([]coordinator.KeyboardButton(nil), row...)
	}
	return &clone
}
func (sender *Sender) SendStatus(ctx context.Context, operationID string, status coordinator.Status) (coordinator.Receipt, error) {
	if _, ok := sender.pending.peek(operationID); ok {
		return coordinator.Receipt{}, errors.New("prepared keyboard operation cannot be sent without its keyboard")
	}
	return sender.base.SendStatus(ctx, operationID, status)
}
func (sender *Sender) SendStatusWithKeyboard(ctx context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return sender.sendPrepared(ctx, operationID, status, keyboard, false)
}
func (sender *Sender) EditStatusWithKeyboard(ctx context.Context, operationID string, status coordinator.Status, keyboard *coordinator.KeyboardMarkup) (coordinator.Receipt, error) {
	return sender.sendPrepared(ctx, operationID, status, keyboard, true)
}
func (sender *Sender) sendPrepared(
	ctx context.Context,
	operationID string,
	status coordinator.Status,
	keyboard *coordinator.KeyboardMarkup,
	edit bool,
) (coordinator.Receipt, error) {
	prepared, found := sender.pending.peek(operationID)
	operation, durable, loadErr := sender.operations.Load(ctx, operationID)
	if loadErr != nil {
		return coordinator.Receipt{}, fmt.Errorf("load callback send operation: %w", loadErr)
	}
	if durable {
		switch operation.Phase {
		case CallbackReceiptConfirmed, CallbackCommitted:
			if operation.Receipt <= 0 {
				return coordinator.Receipt{}, errors.New("confirmed callback operation has invalid receipt")
			}
			return coordinator.Receipt{MessageID: operation.Receipt}, nil
		case CallbackEffectUnknown, CallbackEffectRetryUnknown, CallbackSendUnknown:
			return coordinator.Receipt{}, fmt.Errorf("%w: %s is %s", telegrampipeline.ErrUnknownOperation, operation.ID, operation.Phase)
		case CallbackPrepared:
			if operation.Prepared == nil {
				return coordinator.Receipt{}, errors.New("durable callback operation has no prepared output")
			}
			if !found {
				prepared = clonePrepared(*operation.Prepared)
				found = true
			}
		case CallbackClaimed:
			return coordinator.Receipt{}, errors.New("callback operation effect is not prepared")
		default:
			return coordinator.Receipt{}, errors.New("callback operation phase is invalid")
		}
	}
	if !found {
		if edit {
			return sender.base.EditStatusWithKeyboard(ctx, operationID, status, keyboard)
		}
		return sender.base.SendStatusWithKeyboard(ctx, operationID, status, keyboard)
	}
	defer sender.pending.remove(operationID)
	if !reflect.DeepEqual(prepared.Status, status) || !reflect.DeepEqual(prepared.Keyboard, keyboard) {
		return coordinator.Receipt{}, errors.New("prepared Telegram operation does not match sender input")
	}
	wantsEdit := prepared.Edit
	if wantsEdit != edit {
		return coordinator.Receipt{}, errors.New("prepared Telegram carrier effect does not match sender method")
	}
	if durable {
		sendUnknown := operation
		sendUnknown.Phase = CallbackSendUnknown
		changed, err := sender.operations.CompareAndSwap(ctx, operationID, CallbackPrepared, sendUnknown)
		if err != nil {
			return coordinator.Receipt{}, fmt.Errorf("fence callback carrier send: %w", err)
		}
		if !changed {
			return coordinator.Receipt{}, fmt.Errorf("%w: callback carrier send phase changed", telegrampipeline.ErrUnknownOperation)
		}
		operation = sendUnknown
	}
	var receipt coordinator.Receipt
	var err error
	if edit {
		receipt, err = sender.base.EditStatusWithKeyboard(ctx, operationID, status, keyboard)
	} else {
		receipt, err = sender.base.SendStatusWithKeyboard(ctx, operationID, status, keyboard)
	}
	if err != nil {
		return coordinator.Receipt{}, err
	}
	if receipt.MessageID <= 0 {
		return coordinator.Receipt{}, errors.New("Telegram carrier returned no positive receipt")
	}
	if prepared.Terminal && receipt.MessageID != prepared.Status.SourceMessageID {
		return coordinator.Receipt{}, errors.New("terminal Telegram edit receipt does not match its source carrier")
	}
	if durable {
		confirmed := operation
		confirmed.Phase = CallbackReceiptConfirmed
		confirmed.Receipt = receipt.MessageID
		changed, err := sender.operations.CompareAndSwap(ctx, operationID, CallbackSendUnknown, confirmed)
		if err != nil {
			return coordinator.Receipt{}, fmt.Errorf("persist callback carrier receipt: %w", err)
		}
		if !changed {
			return coordinator.Receipt{}, fmt.Errorf("%w: callback carrier receipt phase changed", telegrampipeline.ErrUnknownOperation)
		}
		operation = confirmed
	}
	if err := finalizePrepared(ctx, sender.registry, sender.uiState, prepared, receipt.MessageID); err != nil {
		return coordinator.Receipt{}, err
	}
	if durable {
		committed := operation
		committed.Phase = CallbackCommitted
		changed, err := sender.operations.CompareAndSwap(ctx, operationID, CallbackReceiptConfirmed, committed)
		if err != nil {
			return coordinator.Receipt{}, fmt.Errorf("persist committed callback carrier: %w", err)
		}
		if !changed {
			return coordinator.Receipt{}, errors.New("callback operation receipt commit phase changed")
		}
	}
	return receipt, nil
}
func (sender *Sender) RetryUnknownSend(ctx context.Context, operationID string) error {
	operation, found, err := sender.operations.Load(ctx, operationID)
	if err != nil {
		return err
	}
	if !found || operation.Phase != CallbackSendUnknown {
		return errors.New("callback send is not awaiting explicit resolution")
	}
	next := operation
	next.Phase = CallbackPrepared
	changed, err := sender.operations.CompareAndSwap(ctx, operationID, CallbackSendUnknown, next)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback send changed while authorizing retry")
	}
	return nil
}
func (sender *Sender) ConfirmUnknownSend(ctx context.Context, operationID string, receipt coordinator.Receipt) error {
	if receipt.MessageID <= 0 {
		return errors.New("verified Telegram receipt must be positive")
	}
	operation, found, err := sender.operations.Load(ctx, operationID)
	if err != nil {
		return err
	}
	if !found || operation.Phase != CallbackSendUnknown || operation.Prepared == nil {
		return errors.New("callback send is not awaiting explicit resolution")
	}
	if operation.Prepared.Edit && receipt.MessageID != operation.Prepared.Status.SourceMessageID {
		return errors.New("verified callback edit receipt does not match its source carrier")
	}
	confirmed := operation
	confirmed.Phase = CallbackReceiptConfirmed
	confirmed.Receipt = receipt.MessageID
	changed, err := sender.operations.CompareAndSwap(ctx, operationID, CallbackSendUnknown, confirmed)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback send changed while confirming receipt")
	}
	if err := finalizePrepared(ctx, sender.registry, sender.uiState, *confirmed.Prepared, receipt.MessageID); err != nil {
		return err
	}
	committed := confirmed
	committed.Phase = CallbackCommitted
	changed, err = sender.operations.CompareAndSwap(ctx, operationID, CallbackReceiptConfirmed, committed)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("callback send changed while committing verified receipt")
	}
	return nil
}
func finalizePrepared(
	ctx context.Context,
	registry telegrampipeline.CallbackRegistry,
	uiState telegramstate.Store,
	prepared Prepared,
	receipt int64,
) error {
	carrier := telegramstate.Carrier{ChatID: prepared.Status.ConversationID, MessageID: receipt}
	if prepared.Terminal {
		if err := registry.InvalidateCarrier(ctx, carrier); err != nil {
			return fmt.Errorf("invalidate confirmed terminal Telegram carrier: %w", err)
		}
		return nil
	}
	if prepared.Card.SessionID != "" {
		if err := commitCard(ctx, uiState, prepared.Card, carrier); err != nil {
			return err
		}
	}
	if err := telegrampipeline.BindPresentation(ctx, registry, carrier, prepared.Presentation); err != nil {
		return fmt.Errorf("bind confirmed Telegram presentation: %w", err)
	}
	return nil
}
func commitCard(ctx context.Context, uiState telegramstate.Store, output CardOutput, carrier telegramstate.Carrier) error {
	projected := output.Projection.Card
	history := make([]string, len(projected.Pages))
	for index, page := range projected.Pages {
		history[index] = page.Content
	}
	want := telegramstate.Card{
		SessionID: output.SessionID,
		Carrier:   carrier,
		Page: telegramstate.Page{
			Current:      projected.View.Page,
			Total:        projected.View.Pages,
			Anchor:       projected.View.Anchor,
			FollowLatest: projected.View.FollowLatest,
		},
		OptionsExpanded: output.OptionsExpanded,
		History:         history,
	}
	if err := uiState.Update(ctx, func(state *telegramstate.State) error {
		if output.MakeActive {
			state.ActiveSession = output.SessionID
		}
		return state.SetCard(want)
	}); err != nil {
		return fmt.Errorf("commit confirmed Telegram card: %w", err)
	}
	reread, err := uiState.Load(ctx)
	if err != nil {
		return fmt.Errorf("reread confirmed Telegram card: %w", err)
	}
	got, ok := reread.Card(output.SessionID)
	if !ok || !reflect.DeepEqual(got, want) || (output.MakeActive && reread.ActiveSession != output.SessionID) {
		return errors.New("confirmed Telegram card reread mismatch")
	}
	return nil
}
func (store *pendingStore) register(prepared Prepared) error {
	if prepared.OperationID == "" || prepared.Keyboard == nil {
		return errors.New("prepared Telegram operation is incomplete")
	}
	if prepared.Terminal {
		if !prepared.Edit || prepared.Status.SourceMessageID <= 0 || len(*prepared.Keyboard) != 0 || prepared.Presentation.SessionID != "" || prepared.Card.SessionID != "" || prepared.Surface != nil {
			return errors.New("prepared terminal Telegram operation is invalid")
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if existing, exists := store.items[prepared.OperationID]; exists {
			if reflect.DeepEqual(existing, prepared) {
				return nil
			}
			return errors.New("prepared Telegram operation already exists")
		}
		store.items[prepared.OperationID] = clonePrepared(prepared)
		return nil
	}
	if prepared.Presentation.SessionID == "" {
		return errors.New("prepared Telegram presentation is required")
	}
	cardPrepared := prepared.Card.SessionID != ""
	surfacePrepared := prepared.Surface != nil
	if cardPrepared == surfacePrepared {
		return errors.New("prepared Telegram operation must contain exactly one card or surface")
	}
	if cardPrepared && domain.SessionID(prepared.Presentation.SessionID) != prepared.Card.SessionID {
		return errors.New("prepared Telegram card and presentation identity do not match")
	}
	if surfacePrepared {
		interaction := prepared.Surface.InteractionSessionID != "" || prepared.Surface.InteractionRequestID != ""
		outboundResolution := prepared.Surface.OutboundOperationID != "" || prepared.Surface.OutboundUpdateID != 0
		recovery := prepared.Surface.Recovery != nil
		acceptedTurnRecovery := prepared.Surface.AcceptedTurnRecovery != nil
		statusRecovery := prepared.Surface.StatusRecovery != nil
		artifactRetry := prepared.Surface.ArtifactRetry != nil
		if telegramrecovery.MultipleBindings(interaction, outboundResolution, recovery, acceptedTurnRecovery, statusRecovery, artifactRetry) {
			return errors.New("prepared Telegram surface has conflicting bindings")
		}
		if interaction {
			if domain.SessionID(prepared.Presentation.SessionID) != prepared.Surface.InteractionSessionID ||
				prepared.Presentation.InteractionRequestID != prepared.Surface.InteractionRequestID {
				return errors.New("prepared Telegram interaction surface identity is invalid")
			}
		} else if outboundResolution {
			if prepared.Presentation.SessionID != telegramui.GlobalSurfaceID ||
				prepared.Presentation.OutboundOperationID != prepared.Surface.OutboundOperationID ||
				prepared.Presentation.OutboundUpdateID != prepared.Surface.OutboundUpdateID {
				return errors.New("prepared Telegram outbound resolution identity is invalid")
			}
		} else if recovery {
			binding := prepared.Presentation.Recovery
			if prepared.Presentation.SessionID != telegramui.GlobalSurfaceID || binding == nil ||
				binding.OperationID != prepared.Surface.Recovery.OperationID || binding.UpdateID != prepared.Surface.Recovery.UpdateID ||
				binding.SessionID != string(prepared.Surface.Recovery.SessionID) || binding.ChatID != prepared.Surface.Recovery.Carrier.ChatID ||
				binding.MessageID != prepared.Surface.Recovery.Carrier.MessageID || binding.Phase != string(prepared.Surface.Recovery.Phase) {
				return errors.New("prepared Telegram callback recovery identity is invalid")
			}
		} else if acceptedTurnRecovery {
			binding := prepared.Presentation.AcceptedTurnRecovery
			if binding == nil || prepared.Presentation.SessionID != string(prepared.Surface.AcceptedTurnRecovery.SessionID) ||
				binding.SessionID != prepared.Surface.AcceptedTurnRecovery.SessionID ||
				binding.MessageID != prepared.Surface.AcceptedTurnRecovery.MessageID ||
				binding.BindingGeneration != prepared.Surface.AcceptedTurnRecovery.BindingGeneration {
				return errors.New("prepared Telegram accepted-turn recovery identity is invalid")
			}
		} else if statusRecovery {
			binding := prepared.Presentation.StatusRecovery
			if prepared.Presentation.SessionID != telegramui.GlobalSurfaceID || binding == nil || *binding != *prepared.Surface.StatusRecovery {
				return errors.New("prepared Telegram status recovery identity is invalid")
			}
		} else if artifactRetry {
			binding := prepared.Presentation.ArtifactRetry
			if binding == nil || prepared.Presentation.SessionID != prepared.Surface.ArtifactRetry.PresentationID || *binding != *prepared.Surface.ArtifactRetry {
				return errors.New("prepared Telegram artifact retry identity is invalid")
			}
		} else if prepared.Presentation.SessionID != telegramui.GlobalSurfaceID || prepared.Presentation.InteractionRequestID != "" ||
			prepared.Presentation.OutboundOperationID != "" || prepared.Presentation.OutboundUpdateID != 0 || prepared.Presentation.Recovery != nil ||
			prepared.Presentation.AcceptedTurnRecovery != nil || prepared.Presentation.StatusRecovery != nil || prepared.Presentation.ArtifactRetry != nil {
			return errors.New("prepared Telegram surface identity is invalid")
		}
	}
	if !reflect.DeepEqual(coordinatorKeyboard(prepared.Presentation.Markup), prepared.Keyboard) {
		return errors.New("prepared Telegram card and presentation do not match")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, exists := store.items[prepared.OperationID]; exists {
		if reflect.DeepEqual(existing, prepared) {
			return nil
		}
		return errors.New("prepared Telegram operation already exists")
	}
	store.items[prepared.OperationID] = clonePrepared(prepared)
	return nil
}
func (store *pendingStore) peek(operationID string) (Prepared, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	prepared, ok := store.items[operationID]
	return clonePrepared(prepared), ok
}
func (store *pendingStore) remove(operationID string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.items, operationID)
}
func coordinatorKeyboard(markup telegram.InlineKeyboardMarkup) *coordinator.KeyboardMarkup {
	keyboard := make(coordinator.KeyboardMarkup, len(markup.InlineKeyboard))
	for rowIndex, row := range markup.InlineKeyboard {
		keyboard[rowIndex] = make([]coordinator.KeyboardButton, len(row))
		for buttonIndex, button := range row {
			keyboard[rowIndex][buttonIndex] = coordinator.KeyboardButton{
				Text:         button.Text,
				CallbackData: button.CallbackData,
			}
		}
	}
	return &keyboard
}
func clonePrepared(prepared Prepared) Prepared {
	clone := prepared
	if prepared.Keyboard != nil {
		keyboard := make(coordinator.KeyboardMarkup, len(*prepared.Keyboard))
		for rowIndex, row := range *prepared.Keyboard {
			keyboard[rowIndex] = append([]coordinator.KeyboardButton(nil), row...)
		}
		clone.Keyboard = &keyboard
	}
	clone.Presentation.Markup = cloneTelegramMarkup(prepared.Presentation.Markup)
	clone.Presentation.TokenIDs = append([]string(nil), prepared.Presentation.TokenIDs...)
	clone.Presentation.Recovery = clonePointer(prepared.Presentation.Recovery)
	clone.Presentation.AcceptedTurnRecovery = clonePointer(prepared.Presentation.AcceptedTurnRecovery)
	clone.Presentation.StatusRecovery = clonePointer(prepared.Presentation.StatusRecovery)
	clone.Presentation.ArtifactRetry = clonePointer(prepared.Presentation.ArtifactRetry)
	clone.Card = cloneCardOutput(prepared.Card)
	if prepared.Surface != nil {
		surface := cloneSurfaceOutput(*prepared.Surface)
		clone.Surface = &surface
	}
	return clone
}
func cloneSurfaceOutput(output SurfaceOutput) SurfaceOutput {
	clone := output
	clone.Recovery = clonePointer(output.Recovery)
	clone.AcceptedTurnRecovery = clonePointer(output.AcceptedTurnRecovery)
	clone.StatusRecovery = clonePointer(output.StatusRecovery)
	clone.ArtifactRetry = clonePointer(output.ArtifactRetry)
	clone.SelectableSessionIDs = append([]domain.SessionID(nil), output.SelectableSessionIDs...)
	clone.Keyboard.Rows = make([]telegramui.ButtonRow, len(output.Keyboard.Rows))
	for index, row := range output.Keyboard.Rows {
		clone.Keyboard.Rows[index] = append(telegramui.ButtonRow(nil), row...)
	}
	return clone
}
func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
func cloneCardOutput(output CardOutput) CardOutput {
	clone := output
	clone.SelectableSessionIDs = append([]domain.SessionID(nil), output.SelectableSessionIDs...)
	projection := output.Projection
	projection.Card.Pages = make([]telegramui.ContentPage, len(output.Projection.Card.Pages))
	for index, page := range output.Projection.Card.Pages {
		projection.Card.Pages[index] = page
		projection.Card.Pages[index].Anchors = append([]string(nil), page.Anchors...)
	}
	projection.Card.Keyboard.Rows = make([]telegramui.ButtonRow, len(output.Projection.Card.Keyboard.Rows))
	for index, row := range output.Projection.Card.Keyboard.Rows {
		projection.Card.Keyboard.Rows[index] = append(telegramui.ButtonRow(nil), row...)
	}
	if output.Projection.Notification != nil {
		notification := *output.Projection.Notification
		projection.Notification = &notification
	}
	clone.Projection = projection
	return clone
}
func cloneTelegramMarkup(markup telegram.InlineKeyboardMarkup) telegram.InlineKeyboardMarkup {
	clone := telegram.InlineKeyboardMarkup{InlineKeyboard: make([][]telegram.InlineKeyboardButton, len(markup.InlineKeyboard))}
	for index, row := range markup.InlineKeyboard {
		clone.InlineKeyboard[index] = append([]telegram.InlineKeyboardButton(nil), row...)
	}
	return clone
}
