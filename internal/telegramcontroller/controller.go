package telegramcontroller

import (
	"bria/internal/app"
	"bria/internal/cardpageselection"
	"bria/internal/cardtranscript"
	"bria/internal/controllertelemetry"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/nativeapprovalflow"
	"bria/internal/promptpreprocess"
	"bria/internal/sessioncloseflow"
	"bria/internal/sessioncreation"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramcontrolport"
	"bria/internal/telegramcreationview"
	"bria/internal/telegramnodes"
	"bria/internal/telegramsemantic"
	"bria/internal/telegramsessions"
	"bria/internal/telegramsessionview"
	"bria/internal/telegramsettings"
	"bria/internal/telegramsettingsview"
	"bria/internal/telegramstatus"
	"bria/internal/telegramturnhelpers"
	"bria/internal/turnadmission"
	"bria/internal/turncompletion"
	"bria/internal/turnprocessing"
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultQueueLimit = 16

var errProviderUnavailable = errors.New("provider is not configured and enabled")

type SessionCreator = telegramcontrolport.SessionCreator
type PendingSessionStart = telegramcontrolport.PendingSessionStart
type SessionStartOutcome = telegramcontrolport.SessionStartOutcome
type AsyncSessionCreator = telegramcontrolport.AsyncSessionCreator
type SessionStore = telegramcontrolport.SessionStore
type SessionNamer = telegramcontrolport.SessionNamer
type Notifier = telegramcontrolport.Notifier
type DeliveryState = telegramcontrolport.DeliveryState
type NotificationFailure = telegramcontrolport.NotificationFailure
type OutputFailureRecorder = telegramcontrolport.OutputFailureRecorder
type ActiveSessionStore = telegramcontrolport.ActiveSessionStore
type CardCarrierStore = telegramcontrolport.CardCarrierStore
type CardPageStore = telegramcontrolport.CardPageStore
type CardHistoryStore = telegramcontrolport.CardHistoryStore
type CardPromptStore = telegramcontrolport.CardPromptStore
type ActiveSessionLoader = telegramcontrolport.ActiveSessionLoader
type Preferences = telegramcontrolport.Preferences
type PreferenceSnapshot = telegramcontrolport.PreferenceSnapshot
type ProviderPreference = telegramcontrolport.ProviderPreference
type ProviderPreferences = telegramcontrolport.ProviderPreferences
type Lifecycle = telegramcontrolport.Lifecycle
type ArchivedResumer = telegramcontrolport.ArchivedResumer
type AsyncArchivedResumer = telegramcontrolport.AsyncArchivedResumer
type SessionCloser = telegramcontrolport.SessionCloser
type InteractiveSessionCloser = telegramcontrolport.InteractiveSessionCloser
type TurnLifecycle = telegramcontrolport.TurnLifecycle
type NotificationKind = telegramcontrolport.NotificationKind
type Notification = telegramcontrolport.Notification
type OutgoingNotification = telegramcontrolport.OutgoingNotification
type OutputReceipt = telegramcontrolport.OutputReceipt
type DurableOutputCustody = telegramcontrolport.DurableOutputCustody
type AuthorizationStart = telegramcontrolport.AuthorizationStart
type AuthorizationChallenge = telegramcontrolport.AuthorizationChallenge
type AuthorizationSecret = telegramcontrolport.AuthorizationSecret
type AuthorizationResult = telegramcontrolport.AuthorizationResult
type AuthorizationPendingLookup = telegramcontrolport.AuthorizationPendingLookup
type PendingAuthorization = telegramcontrolport.PendingAuthorization
type AuthorizationDiscard = telegramcontrolport.AuthorizationDiscard
type AuthorizationMessageLookup = telegramcontrolport.AuthorizationMessageLookup
type AuthorizationMessageBinding = telegramcontrolport.AuthorizationMessageBinding
type AuthorizationFlow = telegramcontrolport.AuthorizationFlow

const (
	DeliveryUnknown          = telegramcontrolport.DeliveryUnknown
	NotificationCommentary   = telegramcontrolport.NotificationCommentary
	NotificationQuestion     = telegramcontrolport.NotificationQuestion
	NotificationFinal        = telegramcontrolport.NotificationFinal
	NotificationError        = telegramcontrolport.NotificationError
	NotificationPromptStatus = telegramcontrolport.NotificationPromptStatus
	NotificationNativeScreen = telegramcontrolport.NotificationNativeScreen
)

type SemanticActionKind = telegramcontrolport.SemanticActionKind

const (
	SemanticPagePrevious                     = telegramsemantic.SemanticPagePrevious
	SemanticPageLatest                       = telegramsemantic.SemanticPageLatest
	SemanticPageNext                         = telegramsemantic.SemanticPageNext
	SemanticStop                             = telegramsemantic.SemanticStop
	SemanticClose                            = telegramsemantic.SemanticClose
	SemanticOptions                          = telegramsemantic.SemanticOptions
	SemanticModelMenu                        = telegramsemantic.SemanticModelMenu
	SemanticNativeKey                        = telegramsemantic.SemanticNativeKey
	SemanticModelChoice                      = telegramsemantic.SemanticModelChoice
	SemanticEffortMenu                       = telegramsemantic.SemanticEffortMenu
	SemanticEffortChoice                     = telegramsemantic.SemanticEffortChoice
	SemanticScreen                           = telegramsemantic.SemanticScreen
	SemanticSelect                           = telegramsemantic.SemanticSelect
	SemanticResume                           = telegramsemantic.SemanticResume
	SemanticMenuSessions                     = telegramsemantic.SemanticMenuSessions
	SemanticMenuNew                          = telegramsemantic.SemanticMenuNew
	SemanticMenuArchive                      = telegramsemantic.SemanticMenuArchive
	SemanticMenuStatus                       = telegramsemantic.SemanticMenuStatus
	SemanticRefreshStatus                    = telegramsemantic.SemanticRefreshStatus
	SemanticMenuSettings                     = telegramsemantic.SemanticMenuSettings
	SemanticMenuBack                         = telegramsemantic.SemanticMenuBack
	SemanticMenuNodes                        = telegramsemantic.SemanticMenuNodes
	SemanticSelectNode                       = telegramsemantic.SemanticSelectNode
	SemanticCreateSelectCodex                = telegramsemantic.SemanticCreateSelectCodex
	SemanticCreateSelectClaude               = telegramsemantic.SemanticCreateSelectClaude
	SemanticCreateWorkdir                    = telegramsemantic.SemanticCreateWorkdir
	SemanticCreateConfirm                    = telegramsemantic.SemanticCreateConfirm
	SemanticCreateChoice                     = telegramsemantic.SemanticCreateChoice
	SemanticCreatePrevious                   = telegramsemantic.SemanticCreatePrevious
	SemanticCreateFirst                      = telegramsemantic.SemanticCreateFirst
	SemanticCreateNext                       = telegramsemantic.SemanticCreateNext
	SemanticCreateUp                         = telegramsemantic.SemanticCreateUp
	SemanticCreatePick                       = telegramsemantic.SemanticCreatePick
	SemanticCreateDirectoryNew               = telegramsemantic.SemanticCreateDirectoryNew
	SemanticCreateBack                       = telegramsemantic.SemanticCreateBack
	SemanticCreateFresh                      = telegramsemantic.SemanticCreateFresh
	SemanticCreateCodex                      = telegramsemantic.SemanticCreateCodex
	SemanticCreateClaude                     = telegramsemantic.SemanticCreateClaude
	SemanticSettingsCategory                 = telegramsemantic.SemanticSettingsCategory
	SemanticSettingsScreen                   = telegramsemantic.SemanticSettingsScreen
	SemanticSettingsScreenCaptureLimit       = telegramsemantic.SemanticSettingsScreenCaptureLimit
	SemanticSettingsAutoApproveCommands      = telegramsemantic.SemanticSettingsAutoApproveCommands
	SemanticSettingsDetail                   = telegramsemantic.SemanticSettingsDetail
	SemanticSettingsPageLimit                = telegramsemantic.SemanticSettingsPageLimit
	SemanticSettingsContinueExisting         = telegramsemantic.SemanticSettingsContinueExisting
	SemanticSettingsTechnicalActions         = telegramsemantic.SemanticSettingsTechnicalActions
	SemanticSettingsTechnicalOutputLines     = telegramsemantic.SemanticSettingsTechnicalOutputLines
	SemanticSettingsTechnicalCommandLines    = telegramsemantic.SemanticSettingsTechnicalCommandLines
	SemanticSettingsBackgroundQuestions      = telegramsemantic.SemanticSettingsBackgroundQuestions
	SemanticSettingsBackgroundErrors         = telegramsemantic.SemanticSettingsBackgroundErrors
	SemanticSettingsArchiveRecommendations   = telegramsemantic.SemanticSettingsArchiveRecommendations
	SemanticSettingsDefaultProvider          = telegramsemantic.SemanticSettingsDefaultProvider
	SemanticSettingsDefaultWorkdir           = telegramsemantic.SemanticSettingsDefaultWorkdir
	SemanticSettingsClearCreationDefaults    = telegramsemantic.SemanticSettingsClearCreationDefaults
	SemanticSettingsLifetimeNever            = telegramsemantic.SemanticSettingsLifetimeNever
	SemanticSettingsLifetime6Hours           = telegramsemantic.SemanticSettingsLifetime6Hours
	SemanticSettingsLifetime12Hours          = telegramsemantic.SemanticSettingsLifetime12Hours
	SemanticSettingsLifetime24Hours          = telegramsemantic.SemanticSettingsLifetime24Hours
	SemanticSettingsLifetime48Hours          = telegramsemantic.SemanticSettingsLifetime48Hours
	SemanticSettingsProviderCodex            = telegramsemantic.SemanticSettingsProviderCodex
	SemanticSettingsProviderClaude           = telegramsemantic.SemanticSettingsProviderClaude
	SemanticSettingsPreprocessing            = telegramsemantic.SemanticSettingsPreprocessing
	SemanticSettingsPreprocessingInstruction = telegramsemantic.SemanticSettingsPreprocessingInstruction
	SemanticSettingsPreprocessingReset       = telegramsemantic.SemanticSettingsPreprocessingReset
	SemanticSettingsSessionNaming            = telegramsemantic.SemanticSettingsSessionNaming
	SemanticSettingsStandby                  = telegramsemantic.SemanticSettingsStandby
	SemanticSettingsRenameNode               = telegramsemantic.SemanticSettingsRenameNode
	SemanticAuthorizeCodex                   = telegramsemantic.SemanticAuthorizeCodex
	SemanticAuthorizeClaude                  = telegramsemantic.SemanticAuthorizeClaude
)

type SemanticAction = telegramcontrolport.SemanticAction
type SemanticCarrierEffect = telegramcontrolport.SemanticCarrierEffect
type SemanticContentPage = telegramcontrolport.SemanticContentPage
type SemanticPageView = telegramcontrolport.SemanticPageView
type SemanticCard = telegramcontrolport.SemanticCard
type SemanticActionResult = telegramcontrolport.SemanticActionResult
type SemanticButton = telegramcontrolport.SemanticButton
type SemanticSurface = telegramcontrolport.SemanticSurface

const SemanticEditSameCarrier = telegramcontrolport.SemanticEditSameCarrier

// ProjectCurrent returns a read-only exact-session or global-active projection.
func (controller *Controller) ProjectCurrent(ctx context.Context, sessionID domain.SessionID) (result SemanticActionResult, err error) {
	defer func() { controller.projectedEvent(ctx, sessionID, result, err) }()
	if sessionID == "" {
		if !controller.nodeAvailable(ctx, controller.currentNodeID()) {
			controller.cancelCreateDraft()
			return controller.nodeListSemanticResult(ctx)
		}
		sessionID, err = controller.ensureCurrentActive(ctx)
		if err != nil {
			return SemanticActionResult{}, err
		}
	}
	if sessionID == "" {
		return controller.sessionListSemanticResult(ctx)
	}
	if native, ok := controller.currentNativeSurface(sessionID); ok {
		return native, nil
	}
	card, err := controller.semanticCard(ctx, sessionID, false)
	return SemanticActionResult{Card: &card}, err
}

// ProjectCompletion returns the completed card and active state without Telegram mutation.
func (controller *Controller) ProjectCompletion(ctx context.Context, sessionID domain.SessionID) (SemanticCard, bool, error) {
	if sessionID == "" {
		return SemanticCard{}, false, errors.New("completion session is required")
	}
	controller.mu.Lock()
	active := controller.active == sessionID && controller.nativeOverlay != sessionID
	controller.mu.Unlock()
	card, err := controller.semanticCard(ctx, sessionID, active)
	return card, active, err
}

// HandleSemanticMessage executes unsigned input and returns neutral data for signed UI composition.
func (controller *Controller) handleSemanticMessage(ctx context.Context, update coordinator.Update) (SemanticActionResult, error) {
	if update.Kind != coordinator.UpdateMessage {
		return SemanticActionResult{}, errors.New("semantic message handler requires a message update")
	}
	nativeInput := controller.isNativeInput(update)
	createDraftRevision := controller.createFlow.Revision()
	decision, err := controller.Handle(ctx, update)
	if err != nil {
		return SemanticActionResult{}, err
	}
	result := SemanticActionResult{Decision: decision}
	if nativeInput && decision.Kind == coordinator.DecisionStatus {
		return controller.nativeMessageSurface(decision.Status.Text), nil
	}
	createDraftChanged := controller.createFlow.Revision() != createDraftRevision
	if createDraftChanged {
		surface, surfaceErr := controller.currentCreateDraftSurfaceV2(ctx)
		result.Surface = surface
		return result, surfaceErr
	}
	messageID := "telegram-update:" + strconv.FormatInt(update.ID, 10)
	controller.mu.Lock()
	promptSession := controller.promptSessions[messageID]
	controller.mu.Unlock()
	if promptSession != "" {
		card, cardErr := controller.semanticCard(ctx, promptSession, true)
		result.Card = &card
		return result, cardErr
	}
	if decision.Kind != coordinator.DecisionStatus {
		return result, nil
	}
	text := strings.TrimSpace(update.Text)
	switch text {
	case "/menu":
		result.Surface = mainMenuSurface(decision.Status.Text)
		return result, nil
	case "/sessions":
		return controller.openSessionsSemanticResult(ctx)
	case "/status":
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active == "" {
			result.Surface = mainMenuSurface(decision.Status.Text)
			return result, nil
		}
		card, cardErr := controller.semanticCard(ctx, active, false)
		result.Card = &card
		return result, cardErr
	}
	if _, _, ok := telegramturnhelpers.ParseNew(text); ok {
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active != "" {
			card, cardErr := controller.semanticCard(ctx, active, true)
			result.Card = &card
			return result, cardErr
		}
	}
	if sessionID, ok := telegramturnhelpers.ParseUse(text); ok {
		if _, loadErr := controller.sessions.Load(ctx, sessionID); loadErr == nil {
			card, cardErr := controller.semanticCard(ctx, sessionID, true)
			result.Card = &card
			return result, cardErr
		}
	}
	result.Surface = &SemanticSurface{Text: decision.Status.Text}
	return result, nil
}

// HandleSemanticAction applies a callback action only after an outer signed,
// one-time callback boundary has authenticated it.
func (controller *Controller) handleSemanticAction(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	if err := validateSemanticAction(action); err != nil {
		return SemanticActionResult{}, err
	}
	if action.Kind != SemanticSettingsPreprocessingInstruction {
		controller.mu.Lock()
		controller.preprocessingInstructionPending = false
		if action.Kind != SemanticSettingsRenameNode {
			controller.nodeRenamePending = false
		}
		controller.mu.Unlock()
	}
	if isGlobalSemanticAction(action.Kind) {
		controller.mu.Lock()
		controller.nativeOverlay = ""
		controller.mu.Unlock()
		return controller.handleGlobalSemanticAction(ctx, action)
	}
	var decision coordinator.Decision
	var err error
	makeActive := false
	switch action.Kind {
	case SemanticNativeKey:
		return controller.nativeKey(ctx, action)
	case SemanticModelMenu, SemanticModelChoice, SemanticEffortMenu, SemanticEffortChoice:
		return controller.modelNotice(action.SessionID, "Старый выбор модели больше не используется. Отправь /model в CLI."), nil
	case SemanticPagePrevious:
		decision, err = controller.cardDecision(ctx, action.SessionID, "pg:prev")
	case SemanticPageLatest:
		decision, err = controller.cardDecision(ctx, action.SessionID, "pg:jump")
	case SemanticPageNext:
		decision, err = controller.cardDecision(ctx, action.SessionID, "pg:next")
	case SemanticStop:
		decision = controller.stopSession(ctx, action.SessionID)
	case SemanticClose:
		if action.Choice == 1 {
			controller.mu.Lock()
			delete(controller.closeConfirmation, action.SessionID)
			controller.mu.Unlock()
			if interactive, ok := controller.sessionCloser.(InteractiveSessionCloser); ok {
				decision, err = controller.beginInteractiveClose(ctx, action.SessionID, interactive)
			} else {
				decision, err = controller.CloseSession(ctx, action.SessionID)
			}
			if err != nil {
				return SemanticActionResult{}, err
			}
			closed, loadErr := controller.sessions.Load(ctx, action.SessionID)
			// BeginClose may return a scheduled result even though its async
			// archive has already committed. Reconcile that durable outcome
			// before choosing a projection from the controller's cached active.
			if loadErr == nil && closed.Status() == domain.SessionArchived {
				if err := controller.applyClosedSession(ctx, closed); err != nil {
					return SemanticActionResult{}, err
				}
			}
			controller.mu.Lock()
			active := controller.active
			controller.mu.Unlock()
			if active == "" {
				return controller.sessionListSemanticResult(ctx)
			}
			card, cardErr := controller.semanticCard(ctx, active, true)
			return SemanticActionResult{Decision: decision, Card: &card}, cardErr
		}
		if action.Choice == 2 {
			controller.mu.Lock()
			delete(controller.closeConfirmation, action.SessionID)
			controller.mu.Unlock()
			decision, err = controller.cardDecision(ctx, action.SessionID, "")
			makeActive = true
			break
		}
		controller.mu.Lock()
		controller.closeConfirmation[action.SessionID] = true
		controller.mu.Unlock()
		card, cardErr := controller.semanticCard(ctx, action.SessionID, true)
		if cardErr != nil {
			return SemanticActionResult{}, cardErr
		}
		return SemanticActionResult{Card: &card}, nil
	case SemanticOptions:
		controller.mu.Lock()
		controller.optionsExpanded[action.SessionID] = !controller.optionsExpanded[action.SessionID]
		controller.mu.Unlock()
		decision, err = controller.cardDecision(ctx, action.SessionID, "")
	case SemanticScreen:
		if controller.settings == nil {
			return SemanticActionResult{}, errors.New("screen settings are not configured")
		}
		if err := controller.settings.ToggleScreen(ctx); err != nil {
			return SemanticActionResult{}, fmt.Errorf("toggle global screen setting: %w", err)
		}
		decision, err = controller.cardDecision(ctx, action.SessionID, "")
	case SemanticSelect:
		decision, err = controller.use(ctx, action.SessionID)
		if err == nil {
			if native, ok := controller.restoreNativeSurface(action.SessionID); ok {
				return native, nil
			}
		}
		makeActive = true
	case SemanticResume:
		decision, err = controller.resumeOrRecover(ctx, action.SessionID)
		makeActive = true
	default:
		return SemanticActionResult{}, fmt.Errorf("unsupported semantic action %q", action.Kind)
	}
	if err != nil {
		return SemanticActionResult{}, err
	}
	card, err := controller.semanticCard(ctx, action.SessionID, makeActive)
	if err != nil {
		return SemanticActionResult{}, err
	}
	return SemanticActionResult{Decision: decision, Card: &card}, nil
}
func isGlobalSemanticAction(kind SemanticActionKind) bool {
	return telegramsemantic.IsGlobal(kind)
}
func (controller *Controller) handleGlobalSemanticAction(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	switch action.Kind {
	case SemanticSettingsAutoApproveCommands:
		if err := controller.toggleNativeAutoApprovals(ctx); err != nil {
			return SemanticActionResult{}, err
		}
		return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryProviders)
	case SemanticMenuSessions:
		controller.clearNodeBack()
		return controller.openSessionsSemanticResult(ctx)
	case SemanticMenuNodes:
		controller.setNodeBack(action.SessionID)
		controller.cancelCreateDraft()
		return controller.nodeListSemanticResult(ctx)
	case SemanticMenuStatus:
		controller.clearNodeBack()
		controller.cancelCreateDraft()
		return controller.statusSemanticResult(ctx)
	case SemanticRefreshStatus:
		controller.cancelCreateDraft()
		return controller.statusSemanticResultRefresh(ctx)
	case SemanticSelectNode:
		return controller.selectNodeSemantic(ctx, action.Choice)
	case SemanticMenuNew:
		controller.setCreationPreferenceMode("")
		if snapshot, open, err := controller.currentCreateSnapshotV2(ctx); err != nil {
			return SemanticActionResult{}, err
		} else if open {
			return controller.advanceCreateV2(ctx, snapshot, 0)
		}
		return controller.beginNewSessionV2(ctx, action.UpdateID)
	case SemanticCreateSelectCodex, SemanticCreateSelectClaude:
		provider := domain.ProviderCodex
		if action.Kind == SemanticCreateSelectClaude {
			provider = domain.ProviderClaude
		}
		if err := controller.selectCreateProviderV2(ctx, provider); err != nil {
			if errors.Is(err, errProviderUnavailable) {
				return SemanticActionResult{Surface: unavailableNewSessionSurface()}, nil
			}
			return SemanticActionResult{}, err
		}
		snapshot, _, err := controller.currentCreateSnapshotV2(ctx)
		if err != nil {
			return SemanticActionResult{}, err
		}
		if controller.currentCreationPreferenceMode() != "" {
			return controller.advanceCreationPreferenceV2(ctx, snapshot)
		}
		return controller.advanceCreateV2(ctx, snapshot, action.UpdateID)
	case SemanticCreateChoice:
		return controller.handleCreateChoiceV2(ctx, action)
	case SemanticCreatePrevious, SemanticCreateFirst, SemanticCreateNext, SemanticCreateUp,
		SemanticCreatePick, SemanticCreateDirectoryNew, SemanticCreateBack, SemanticCreateFresh:
		return controller.handleCreateNavigationV2(ctx, action)
	case SemanticCreateWorkdir:
		if err := controller.beginCreateWorkdir(ctx); err != nil {
			return SemanticActionResult{}, err
		}
		surface, err := controller.currentCreateDraftSurface(ctx)
		return SemanticActionResult{Surface: surface}, err
	case SemanticCreateConfirm:
		return controller.confirmCreateDraft(ctx, action.UpdateID)
	case SemanticMenuArchive:
		controller.clearNodeBack()
		return controller.archiveSemanticResult(ctx, max(1, action.Choice))
	case SemanticMenuSettings:
		controller.clearNodeBack()
		controller.cancelCreateDraft()
		return controller.settingsSemanticResult(ctx)
	case SemanticSettingsCategory:
		return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.Category(action.Choice))
	case SemanticSettingsDefaultProvider:
		return controller.beginCreationPreferenceV2(ctx, creationPreferenceProvider)
	case SemanticSettingsDefaultWorkdir:
		return controller.beginCreationPreferenceV2(ctx, creationPreferenceWorkdir)
	case SemanticSettingsClearCreationDefaults:
		return controller.clearCreationPreferencesV2(ctx)
	case SemanticMenuBack:
		controller.cancelCreateDraft()
		controller.mu.Lock()
		backSession := controller.nodeBackSession
		controller.nodeBackSession = ""
		controller.mu.Unlock()
		if backSession != "" {
			if native, ok := controller.restoreNativeSurface(backSession); ok {
				return native, nil
			}
			card, err := controller.semanticCard(ctx, backSession, true)
			if err == nil {
				return SemanticActionResult{Card: &card}, nil
			}
		}
		return SemanticActionResult{Surface: mainMenuSurface("Меню")}, nil
	case SemanticCreateCodex, SemanticCreateClaude:
		provider := domain.ProviderCodex
		if action.Kind == SemanticCreateClaude {
			provider = domain.ProviderClaude
		}
		if err := controller.selectCreateProvider(ctx, provider); err != nil {
			return SemanticActionResult{Surface: unavailableNewSessionSurface()}, nil
		}
		return controller.confirmCreateDraft(ctx, action.UpdateID)
	case SemanticSettingsScreen, SemanticSettingsScreenCaptureLimit, SemanticSettingsDetail, SemanticSettingsPageLimit, SemanticSettingsContinueExisting, SemanticSettingsTechnicalActions,
		SemanticSettingsTechnicalOutputLines, SemanticSettingsTechnicalCommandLines,
		SemanticSettingsBackgroundQuestions, SemanticSettingsBackgroundErrors,
		SemanticSettingsArchiveRecommendations, SemanticSettingsSessionNaming, SemanticSettingsStandby,
		SemanticSettingsLifetimeNever, SemanticSettingsLifetime6Hours, SemanticSettingsLifetime12Hours,
		SemanticSettingsLifetime24Hours, SemanticSettingsLifetime48Hours, SemanticSettingsProviderCodex, SemanticSettingsProviderClaude:
		if err := telegramsettings.Apply(ctx, controller.settings, controller.scopedProviderPreferences(), string(action.Kind)); err != nil {
			return SemanticActionResult{}, err
		}
		category, ok := telegramsettingsview.CategoryForAction(string(action.Kind))
		if !ok {
			return SemanticActionResult{}, errors.New("settings action has no category")
		}
		if action.Kind == SemanticSettingsProviderCodex || action.Kind == SemanticSettingsProviderClaude {
			controller.preparation.Invalidate(controller.currentNodeID())
		}
		if action.Kind == SemanticSettingsStandby {
			controller.ScheduleStandby()
		}
		return controller.settingsCategorySemanticResult(ctx, category)
	case SemanticSettingsPreprocessingInstruction:
		if _, ok := controller.settings.(settingsport.PreprocessingPreferences); !ok {
			return SemanticActionResult{}, errors.New("preprocessing settings are not configured")
		}
		controller.mu.Lock()
		controller.preprocessingInstructionPending = true
		controller.mu.Unlock()
		return SemanticActionResult{Surface: &SemanticSurface{
			Text: "Отправьте новую инструкцию препроцессинга одним текстовым сообщением.",
			Rows: [][]SemanticButton{{{Label: "Отмена", Action: SemanticMenuSettings}}},
		}}, nil
	case SemanticSettingsRenameNode:
		if _, ok := controller.providerPreferences.(settingsport.NodeRenamer); !ok {
			return SemanticActionResult{}, errors.New("переименование ноды не настроено")
		}
		controller.mu.Lock()
		controller.nodeRenamePending = true
		controller.mu.Unlock()
		return SemanticActionResult{Surface: &SemanticSurface{Text: "Отправьте новое имя ноды одним текстовым сообщением (до 64 символов).", Rows: [][]SemanticButton{{{Label: "Отмена", Action: SemanticMenuSettings}}}}}, nil
	case SemanticSettingsPreprocessing, SemanticSettingsPreprocessingReset:
		if err := telegramsettings.Apply(ctx, controller.settings, controller.scopedProviderPreferences(), string(action.Kind)); err != nil {
			return SemanticActionResult{}, err
		}
		controller.preparation.Invalidate(controller.currentNodeID())
		return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryPreprocessing)
	case SemanticAuthorizeCodex, SemanticAuthorizeClaude:
		provider := domain.ProviderCodex
		if action.Kind == SemanticAuthorizeClaude {
			provider = domain.ProviderClaude
		}
		return controller.startAuthorization(ctx, action.UpdateID, provider)
	default:
		return SemanticActionResult{}, fmt.Errorf("unsupported global semantic action %q", action.Kind)
	}
}
func mainMenuSurface(text string) *SemanticSurface {
	return &SemanticSurface{Text: text, Rows: [][]SemanticButton{
		{{Label: "Сессии", Action: SemanticMenuSessions}, {Label: "Архив", Action: SemanticMenuArchive}},
		{{Label: "Ноды", Action: SemanticMenuStatus}, {Label: "➕ Новая", Action: SemanticMenuNew}},
		{{Label: "Настройки", Action: SemanticMenuSettings}},
	}}
}
func (controller *Controller) newSessionSurface(ctx context.Context) (*SemanticSurface, error) {
	providers, err := controller.availableProviders(ctx)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return unavailableNewSessionSurface(), nil
	}
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions for creation defaults: %w", err)
	}
	controller.createFlow.Begin(controller.localComputerID, providers, sessions, "")
	return controller.currentCreateDraftSurface(ctx)
}

func (controller *Controller) currentCreateDraftSurface(ctx context.Context) (*SemanticSurface, error) {
	providers, err := controller.availableProviders(ctx)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return unavailableNewSessionSurface(), nil
	}
	snapshot, open := controller.createFlow.Current(providers)
	if !open {
		return controller.newSessionSurface(ctx)
	}
	view := telegramcreationview.RenderLegacy(snapshot, providers)
	return semanticCreationSurface(view), nil
}

func (controller *Controller) selectCreateProvider(ctx context.Context, provider domain.Provider) error {
	providers, err := controller.availableProviders(ctx)
	if err != nil {
		return err
	}
	if err := controller.createFlow.SelectProvider(provider, providers); err != nil {
		return errProviderUnavailable
	}
	return nil
}

func (controller *Controller) beginCreateWorkdir(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return controller.createFlow.BeginWorkdir()
}

func (controller *Controller) consumeCreateDraftWorkdir(update coordinator.Update) (coordinator.Decision, bool) {
	if !controller.createFlow.ConsumeWorkdir(update.Text, update.MediaKind != "" || update.Caption != "") {
		return coordinator.Decision{}, false
	}
	return controller.status("Рабочая папка обработана."), true
}

func (controller *Controller) confirmCreateDraft(ctx context.Context, updateID int64) (SemanticActionResult, error) {
	providers, err := controller.availableProviders(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	draft, err := controller.createFlow.Confirm(providers)
	if err != nil {
		return SemanticActionResult{}, err
	}
	decision, err := controller.create(ctx, updateID, draft.ComputerID, draft.Provider, draft.Workdir)
	if errors.Is(err, errProviderUnavailable) {
		return SemanticActionResult{Surface: unavailableNewSessionSurface()}, nil
	}
	if err != nil {
		return SemanticActionResult{}, err
	}
	controller.cancelCreateDraft()
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	card, err := controller.semanticCard(ctx, active, true)
	return SemanticActionResult{Decision: decision, Card: &card}, err
}

func (controller *Controller) cancelCreateDraft() {
	controller.createFlow.Cancel()
	controller.setCreationPreferenceMode("")
}
func (controller *Controller) availableProviders(ctx context.Context) ([]domain.Provider, error) {
	return telegramnodes.AvailableProviders(ctx, controller.providerPreferences)
}
func (controller *Controller) providerEnabled(ctx context.Context, computerID domain.ComputerID, provider domain.Provider) (bool, error) {
	return telegramnodes.ProviderEnabled(ctx, controller.creationEnvironment, controller.localComputerID,
		controller.providerPreferences, computerID, provider)
}
func unavailableNewSessionSurface() *SemanticSurface {
	return &SemanticSurface{Text: "Создание новой сессии недоступно: нет настроенной и включенной CLI.", Rows: [][]SemanticButton{{{Label: "Меню", Action: SemanticMenuBack}}}}
}
func (controller *Controller) sessionListSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	var text strings.Builder
	text.WriteString("Сессии")
	currentNode := controller.currentNodeID()
	labelsByID := telegramsessions.Labels(sessions, currentNode)
	for _, candidate := range sessions {
		if label := controller.standbyLabel(ctx, candidate); label != "" {
			labelsByID[candidate.ID()] = label
		}
	}
	rows := make([][]SemanticButton, 0, (len(sessions)+2)/3+2)
	row := make([]SemanticButton, 0, 3)
	for _, session := range sessions {
		if session.ComputerID() != currentNode || !telegramsessions.Viewable(session.Status()) {
			continue
		}
		fmt.Fprintf(&text, "\n%s %s %s", session.Provider(), labelsByID[session.ID()], session.Status())
		row = append(row, SemanticButton{Label: labelsByID[session.ID()], Action: SemanticSelect, SessionID: session.ID()})
		if len(row) == 3 {
			rows = append(rows, row)
			row = make([]SemanticButton, 0, 3)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []SemanticButton{
		{Label: "➕ Новая", Action: SemanticMenuNew},
		{Label: "Ноды", Action: SemanticMenuNodes},
		{Label: "≡ Меню", Action: SemanticMenuBack},
	})
	return SemanticActionResult{Surface: &SemanticSurface{Text: text.String(), Rows: rows}}, nil
}
func (controller *Controller) startAuthorization(ctx context.Context, updateID int64, provider domain.Provider) (SemanticActionResult, error) {
	if controller.authorization == nil {
		return SemanticActionResult{Surface: &SemanticSurface{Text: "Авторизация через Telegram не настроена."}}, nil
	}
	if !controller.authorization.SupportsAuthorization(provider) {
		return SemanticActionResult{Surface: &SemanticSurface{
			Text: "Авторизация " + string(provider) + " через Telegram недоступна на этом компьютере.",
			Rows: [][]SemanticButton{{{Label: "Назад", Action: SemanticMenuSettings}}},
		}}, nil
	}
	operationID := "telegram-update:" + strconv.FormatInt(updateID, 10) + ":authorization"
	challenge, err := controller.authorization.StartAuthorization(ctx, AuthorizationStart{
		OperationID: operationID, ActorID: controller.ownerUserID,
		PrivateChatID: controller.ownerPrivateChatID, ConversationKind: "private",
		ComputerID: controller.localComputerID, Provider: provider,
	})
	if err != nil {
		return SemanticActionResult{Surface: &SemanticSurface{Text: "Не удалось начать авторизацию. Секретные данные не принимались."}}, nil
	}
	if challenge.OperationID != operationID || challenge.ComputerID != controller.localComputerID ||
		challenge.Provider != provider || strings.TrimSpace(challenge.ChallengeReference) == "" ||
		strings.TrimSpace(challenge.Instruction) == "" {
		return SemanticActionResult{}, errors.New("authorization flow returned an inconsistent safe challenge")
	}
	challenge.Instruction = strings.TrimSpace(challenge.Instruction)
	controller.mu.Lock()
	copyChallenge := challenge
	controller.pendingAuthorization = &copyChallenge
	controller.mu.Unlock()
	return SemanticActionResult{Surface: &SemanticSurface{Text: challenge.Instruction}}, nil
}
func (controller *Controller) submitAuthorization(ctx context.Context, update coordinator.Update, challenge AuthorizationChallenge) (coordinator.Decision, error) {
	if update.SourceMessageID <= 0 {
		return coordinator.Decision{}, errors.New("authorization secret message cannot be addressed")
	}
	secret := []byte(update.Text)
	defer func() {
		for index := range secret {
			secret[index] = 0
		}
	}()
	result, err := controller.authorization.SubmitAuthorization(ctx, AuthorizationSecret{
		OperationID: challenge.OperationID, SubmissionOperationID: "telegram-message:" + strconv.FormatInt(update.SourceMessageID, 10) + ":authorization",
		ActorID: controller.ownerUserID, PrivateChatID: controller.ownerPrivateChatID,
		ConversationKind: "private", SourceMessageID: update.SourceMessageID,
		ComputerID: challenge.ComputerID, Provider: challenge.Provider,
		ChallengeReference: challenge.ChallengeReference, Secret: secret,
	})
	if err != nil || !result.Authenticated {
		if !result.DeletionKnown {
			return coordinator.Decision{}, errors.New("authorization secret deletion remains unconfirmed")
		}
		return controller.status("Авторизация не подтверждена. Сообщение с секретом удалено."), nil
	}
	controller.mu.Lock()
	if controller.pendingAuthorization != nil && controller.pendingAuthorization.OperationID == challenge.OperationID {
		controller.pendingAuthorization = nil
	}
	controller.mu.Unlock()
	controller.preparation.Invalidate(controller.currentNodeID())
	return controller.status(authorizationProviderName(challenge.Provider) + " авторизован. Сообщение с секретом удалено."), nil
}
func authorizationProviderName(provider domain.Provider) string {
	switch provider {
	case domain.ProviderCodex:
		return "Codex"
	case domain.ProviderClaude:
		return "Claude"
	default:
		return "Провайдер"
	}
}
func (controller *Controller) consumeAuthorizationMessage(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool, error) {
	if controller.authorization == nil || update.SourceMessageID <= 0 {
		return coordinator.Decision{}, false, nil
	}
	binding, err := controller.authorization.ConsumeAuthorizationMessage(ctx, AuthorizationMessageLookup{
		ActorID: update.ActorID, PrivateChatID: update.ConversationID,
		ConversationKind: update.ConversationKind, SourceMessageID: update.SourceMessageID,
	})
	if err != nil {
		return coordinator.Decision{}, true, errors.New("authorization message tombstone is unavailable")
	}
	if !binding.Bound {
		return coordinator.Decision{}, false, nil
	}
	if !binding.DeletionKnown {
		return coordinator.Decision{}, true, errors.New("authorization message deletion remains unconfirmed")
	}
	if binding.Authenticated {
		if binding.Provider != domain.ProviderCodex && binding.Provider != domain.ProviderClaude {
			return coordinator.Decision{}, true, errors.New("authorization tombstone has invalid provider")
		}
		controller.preparation.Invalidate(controller.currentNodeID())
		return controller.status(authorizationProviderName(binding.Provider) + " авторизован. Сообщение с секретом удалено."), true, nil
	}
	return controller.status("Авторизация не подтверждена. Сообщение с секретом удалено."), true, nil
}
func interactionTextInput(update coordinator.Update) InteractionTextInput {
	return InteractionTextInput{
		ActorID: update.ActorID, ConversationID: update.ConversationID, ConversationKind: update.ConversationKind,
		SourceMessageID: update.SourceMessageID, ReplyToMessageID: update.ReplyToMessageID,
		Text: update.Text, Caption: update.Caption, MediaKind: update.MediaKind,
	}
}
func (controller *Controller) interactionTextDecision(result InteractionTextResult) (coordinator.Decision, error) {
	if result.Secret && !result.DeletionKnown {
		return coordinator.Decision{}, errors.New("pending interaction secret deletion is unconfirmed")
	}
	if strings.TrimSpace(result.Status) == "" {
		return coordinator.Decision{}, errors.New("pending interaction returned no safe status")
	}
	return controller.status(result.Status), nil
}
func (controller *Controller) routeAuthorizationMessage(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool, error) {
	if controller.authorization == nil {
		return coordinator.Decision{}, false, nil
	}
	pending, err := controller.authorization.PendingAuthorizations(ctx, AuthorizationPendingLookup{
		ActorID: controller.ownerUserID, PrivateChatID: controller.ownerPrivateChatID, ConversationKind: "private",
	})
	if err != nil || len(pending) != 1 {
		if err == nil && len(pending) == 0 {
			return coordinator.Decision{}, false, nil
		}
		decision, discardErr := controller.discardAuthorizationMessage(ctx, update, "Нельзя однозначно сопоставить сообщение с авторизацией.")
		return decision, true, discardErr
	}
	operation := pending[0]
	challenge := operation.AuthorizationChallenge
	if update.Text == "" || update.Caption != "" || update.MediaKind != "" {
		decision, discardErr := controller.discardAuthorizationMessage(ctx, update, "Во время авторизации принимается только отдельное текстовое сообщение с секретом.")
		return decision, true, discardErr
	}
	if !operation.AcceptsSecret || challenge.OperationID == "" || challenge.ComputerID != controller.localComputerID ||
		!controller.authorization.SupportsAuthorization(challenge.Provider) || strings.TrimSpace(challenge.ChallengeReference) == "" {
		decision, discardErr := controller.discardAuthorizationMessage(ctx, update, "Авторизация пока не готова принять секрет.")
		return decision, true, discardErr
	}
	controller.mu.Lock()
	copyChallenge := challenge
	controller.pendingAuthorization = &copyChallenge
	controller.mu.Unlock()
	decision, submitErr := controller.submitAuthorization(ctx, update, challenge)
	return decision, true, submitErr
}
func (controller *Controller) discardAuthorizationMessage(ctx context.Context, update coordinator.Update, reason string) (coordinator.Decision, error) {
	if update.SourceMessageID <= 0 {
		return coordinator.Decision{}, errors.New("authorization message deletion cannot be addressed")
	}
	result, err := controller.authorization.DiscardAuthorizationMessage(ctx, AuthorizationDiscard{
		OperationID: "telegram-message:" + strconv.FormatInt(update.SourceMessageID, 10) + ":delete",
		ActorID:     controller.ownerUserID, PrivateChatID: controller.ownerPrivateChatID,
		ConversationKind: "private", SourceMessageID: update.SourceMessageID,
	})
	if err != nil || !result.DeletionKnown {
		return coordinator.Decision{}, errors.New("authorization message deletion remains unconfirmed")
	}
	return controller.status(reason + " Сообщение удалено."), nil
}
func validateSemanticAction(action SemanticAction) error {
	return telegramsemantic.ValidateAction(action)
}

type Options = telegramcontrolport.Options
type Controller struct {
	ownerUserID                     int64
	ownerPrivateChatID              int64
	localComputerID                 domain.ComputerID
	creator                         SessionCreator
	sessions                        SessionStore
	submitter                       sessionruntime.Submitter
	notifier                        Notifier
	lifecycle                       Lifecycle
	uiState                         ActiveSessionStore
	settings                        Preferences
	providerPreferences             ProviderPreferences
	stopper                         sessionruntime.TurnStopper
	preparation                     telegramturnhelpers.Preparation
	durableInput                    DurableInputCustody
	durableOutput                   DurableOutputCustody
	interactions                    InteractionHandler
	interactionText                 InteractionTextHandler
	authorization                   AuthorizationFlow
	attachments                     AttachmentCustody
	runtimeEvents                   RuntimeEventObserver
	finals                          FinalProcessor
	asyncCreator                    AsyncSessionCreator
	archivedResumer                 ArchivedResumer
	recoverer                       SessionRecoverer
	recovering                      map[domain.SessionID]bool
	asyncResumer                    AsyncArchivedResumer
	sessionCloser                   SessionCloser
	closeFlow                       sessioncloseflow.Flow
	turnLifecycle                   TurnLifecycle
	outputFailures                  OutputFailureRecorder
	queueLimit                      int
	rootContext                     context.Context
	cancelRoot                      context.CancelFunc
	mu                              sync.Mutex
	selectionMu                     sync.Mutex
	closed                          bool
	closeDone                       chan struct{}
	closeErr                        error
	active                          domain.SessionID
	nodes                           *telegramnodes.Scope
	live                            map[domain.SessionID]domain.Session
	pending                         map[domain.SessionID]domain.Session
	workers                         map[domain.SessionID]*sessionWorker
	created                         map[domain.SessionID]createdProcess
	history                         map[domain.SessionID][]string
	transcriptKinds                 map[domain.SessionID]map[int]string
	page                            map[domain.SessionID]int
	followLatest                    map[domain.SessionID]bool
	pageAnchor                      map[domain.SessionID]string
	optionsExpanded                 map[domain.SessionID]bool
	closeConfirmation               map[domain.SessionID]bool
	deliveryFailures                map[domain.SessionID]NotificationFailure
	promptIndexes                   map[domain.SessionID]map[string]int
	runtimeTail                     map[domain.SessionID]map[string]int
	technicalHistory                map[domain.SessionID]map[int]bool
	promptSessions                  map[string]domain.SessionID
	pendingAuthorization            *AuthorizationChallenge
	createFlow                      *sessioncreation.Flow
	creationEnvironment             sessioncreation.Environment
	quotas                          telegramstatus.Reader
	quotaRefreshInFlight            bool
	models                          ModelCatalog
	native                          sessionruntime.NativeController
	nativeApprovals                 nativeapprovalflow.Flow
	nativeSnapshots                 map[domain.SessionID]sessionruntime.NativeSnapshot
	nativeOverlay                   domain.SessionID
	nativeCardVisible               bool
	nativeViewContext               context.Context
	nativeViewCancel                context.CancelFunc
	nativeViewSession               domain.SessionID
	finalWrites                     map[domain.SessionID]int
	nativeObserverStarted           bool
	nodeBackSession                 domain.SessionID
	sessionNamer                    SessionNamer
	preprocessingInstructionPending bool
	nodeRenamePending               bool
	creationPreferenceMode          string
	creates                         sync.WaitGroup
	standbyMu                       sync.Mutex
	standbyRequested                bool
	standbyErrors                   map[domain.ComputerID]error
	worker                          sync.WaitGroup
	durableWork                     sync.WaitGroup
}
type createdProcess struct {
	request app.StartSessionRequest
	binding domain.ProviderBinding
}
type queuedTurn struct {
	text        string
	messageID   string
	attachments []AttachmentRef
	admission   *turnadmission.Admission
}
type sessionWorker struct {
	controller        *Controller
	sessionID         domain.SessionID
	queue             chan queuedTurn
	mu                sync.Mutex
	activeCancel      context.CancelFunc
	activeTurn        uint64
	stoppingTurn      uint64
	completion        *turncompletion.Signal
	completionBinding domain.ProviderBinding
	admission         *turnadmission.Admission
}

var _ coordinator.Handler = (*Controller)(nil)

func New(
	ownerUserID int64,
	ownerPrivateChatID int64,
	localComputerID domain.ComputerID,
	creator SessionCreator,
	sessions SessionStore,
	submitter sessionruntime.Submitter,
	notifier Notifier,
	options Options,
) (*Controller, error) {
	if ownerUserID <= 0 || ownerPrivateChatID <= 0 ||
		strings.TrimSpace(string(localComputerID)) == "" {
		return nil, errors.New("owner, private chat, and local computer identities are required")
	}
	if (creator == nil && options.AsyncCreator == nil) || sessions == nil || submitter == nil || notifier == nil {
		return nil, errors.New("creator, session store, submitter, and notifier are required")
	}
	if options.QueueLimit < 0 {
		return nil, errors.New("per-session queue limit must not be negative")
	}
	if options.PreprocessingTimeout < 0 {
		return nil, errors.New("preprocessing timeout must not be negative")
	}
	queueLimit := options.QueueLimit
	if queueLimit == 0 {
		queueLimit = defaultQueueLimit
	}
	preprocessingTimeout := options.PreprocessingTimeout
	if preprocessingTimeout == 0 {
		preprocessingTimeout = 10 * time.Second
	}
	rootContext, cancelRoot := context.WithCancel(context.Background())
	nodes, err := telegramnodes.New(localComputerID, sessions, options.UIState, options.CreationEnvironment)
	if err != nil {
		cancelRoot()
		return nil, fmt.Errorf("create Telegram node scope: %w", err)
	}
	controller := &Controller{
		ownerUserID: ownerUserID, ownerPrivateChatID: ownerPrivateChatID,
		localComputerID: localComputerID, creator: creator, sessions: sessions,
		submitter: submitter, notifier: notifier, lifecycle: options.Lifecycle,
		queueLimit: queueLimit, rootContext: rootContext, cancelRoot: cancelRoot,
		uiState:             options.UIState,
		settings:            options.Settings,
		providerPreferences: options.Providers,
		stopper:             options.Stopper,
		durableInput:        options.DurableInput,
		durableOutput:       options.DurableOutput,
		interactions:        options.Interactions,
		interactionText:     options.InteractionText,
		authorization:       options.Authorization,
		asyncCreator:        options.AsyncCreator,
		archivedResumer:     options.ArchivedResumer,
		recoverer:           options.Recoverer,
		recovering:          make(map[domain.SessionID]bool),
		asyncResumer:        options.AsyncResumer,
		sessionCloser:       options.SessionCloser,
		closeFlow:           sessioncloseflow.Flow{Observer: options.ControllerObserver},
		turnLifecycle:       options.TurnLifecycle,
		attachments:         options.Attachments,
		runtimeEvents:       options.RuntimeEvents,
		finals:              options.Finals,
		outputFailures:      options.OutputFailures,
		closeDone:           make(chan struct{}),
		live:                make(map[domain.SessionID]domain.Session),
		pending:             make(map[domain.SessionID]domain.Session),
		workers:             make(map[domain.SessionID]*sessionWorker),
		created:             make(map[domain.SessionID]createdProcess),
		history:             make(map[domain.SessionID][]string),
		page:                make(map[domain.SessionID]int),
		followLatest:        make(map[domain.SessionID]bool),
		pageAnchor:          make(map[domain.SessionID]string),
		finalWrites:         make(map[domain.SessionID]int),
		optionsExpanded:     make(map[domain.SessionID]bool),
		closeConfirmation:   make(map[domain.SessionID]bool),
		deliveryFailures:    make(map[domain.SessionID]NotificationFailure),
		promptIndexes:       make(map[domain.SessionID]map[string]int),
		runtimeTail:         make(map[domain.SessionID]map[string]int),
		promptSessions:      make(map[string]domain.SessionID),
		nodes:               nodes,
		createFlow:          sessioncreation.New(),
		creationEnvironment: options.CreationEnvironment,
		quotas:              options.Quotas,
		models:              options.Models,
		native:              options.Native,
		nativeSnapshots:     make(map[domain.SessionID]sessionruntime.NativeSnapshot),
		sessionNamer:        options.SessionNamer,
		preparation: telegramturnhelpers.Preparation{
			Settings:           options.Settings,
			Processor:          options.Preprocessor,
			Observer:           options.PreprocessingObserver,
			Timeout:            preprocessingTimeout,
			InputPreparer:      options.InputPreparer,
			AllowDocumentInput: options.AllowDocumentInput,
		},
	}
	for _, session := range options.Recovered {
		if session.Status() == domain.SessionReady {
			controller.live[session.ID()] = session
			controller.ensureWorkerLocked(session.ID())
		}
	}
	if err := controller.restoreNodeSelection(context.Background()); err != nil {
		cancelRoot()
		return nil, fmt.Errorf("restore selected node: %w", err)
	}
	return controller, nil
}
func (controller *Controller) Handle(
	ctx context.Context,
	update coordinator.Update,
) (coordinator.Decision, error) {
	if update.ActorID != controller.ownerUserID ||
		update.ConversationID != controller.ownerPrivateChatID ||
		update.ConversationKind != "private" {
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
	if update.Kind == coordinator.UpdateCallback {
		controller.mu.Lock()
		controller.preprocessingInstructionPending = false
		controller.nodeRenamePending = false
		controller.mu.Unlock()
		return controller.handleCallback(ctx, update)
	}
	if update.Kind != coordinator.UpdateMessage {
		return coordinator.Decision{
			Kind:        coordinator.DecisionBlock,
			BlockReason: "authorized non-text update is not handled by the text controller",
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return coordinator.Decision{}, err
	}
	controller.mu.Lock()
	closed := controller.closed
	controller.mu.Unlock()
	if closed {
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	if decision, handled, err := controller.consumeAuthorizationMessage(ctx, update); handled {
		return decision, err
	}
	if tombstones, ok := controller.interactionText.(InteractionSourceTombstone); ok {
		result, err := tombstones.ConsumeBoundSourceMessage(ctx, interactionTextInput(update))
		if err != nil {
			return coordinator.Decision{}, fmt.Errorf("consume bound interaction message: %w", err)
		}
		if result.Handled {
			return controller.interactionTextDecision(result)
		}
	}
	if decision, handled, err := controller.routeAuthorizationMessage(ctx, update); handled {
		return decision, err
	}
	if controller.isNativeInput(update) {
		controller.cancelCreateDraft()
		controller.mu.Lock()
		controller.preprocessingInstructionPending = false
		id := controller.active
		controller.mu.Unlock()
		text := controller.nativeCommand(ctx, id, update.Text)
		return controller.status(text), nil
	}
	if controller.interactionText != nil {
		result, err := controller.interactionText.ResolvePendingText(ctx, interactionTextInput(update))
		if err != nil {
			return coordinator.Decision{}, fmt.Errorf("resolve pending interaction text: %w", err)
		}
		if result.Handled {
			return controller.interactionTextDecision(result)
		}
	}
	if decision, handled, err := controller.consumePreprocessingInstruction(ctx, update); handled {
		return decision, err
	}
	if decision, handled, err := controller.consumeNodeRename(ctx, update); handled {
		return decision, err
	}
	text := strings.TrimSpace(update.Text)
	switch text {
	case "/menu":
		controller.mu.Lock()
		controller.nativeOverlay = ""
		controller.mu.Unlock()
		controller.cancelCreateDraft()
		return controller.menuStatus("Меню"), nil
	case "/status":
		return controller.mainSurface(ctx)
	case "/sessions":
		controller.mu.Lock()
		hasActive := controller.active != ""
		controller.mu.Unlock()
		if hasActive {
			return controller.mainSurface(ctx)
		}
		return controller.listSessions(ctx)
	case "/stop":
		return controller.stopCurrent(ctx), nil
	}
	if decision, handled := controller.consumeCreateDirectoryNameV2(ctx, update); handled {
		return decision, nil
	}
	if decision, handled := controller.consumeCreateDraftWorkdir(update); handled {
		return decision, nil
	}
	if provider, workdir, ok := telegramturnhelpers.ParseNew(text); ok {
		decision, err := controller.create(ctx, update.ID, controller.currentNodeID(), provider, workdir)
		if errors.Is(err, errProviderUnavailable) {
			return controller.status(unavailableNewSessionSurface().Text), nil
		}
		return decision, err
	}
	if sessionID, ok := telegramturnhelpers.ParseUse(text); ok {
		return controller.use(ctx, sessionID)
	}
	controller.mu.Lock()
	activeSession := controller.active
	controller.mu.Unlock()
	if current, loadErr := controller.sessions.Load(ctx, activeSession); loadErr == nil && current.Status() == domain.SessionAwaitingRecovery {
		return controller.cardDecision(ctx, activeSession, "Исход предыдущего запроса не подтверждён. Доступна история; новый запрос не отправлен. Выбери восстановление или другую сессию.")
	}
	messageID := "telegram-update:" + strconv.FormatInt(update.ID, 10)
	promptText := telegramturnhelpers.JoinPromptParts(update.Text, update.Caption)
	if promptText == "" {
		promptText = telegramturnhelpers.MediaPromptLabel(update.MediaKind)
	}
	if update.MediaKind == "voice" {
		// A default session is a preparation slot: the first request must
		// immediately start provisioning its successor, independently of the
		// slower prompt persistence/transcription path.
		controller.ScheduleStandby()
		if err := controller.publishPromptState(ctx, activeSession, messageID, promptText, "🙋‍♂"); err != nil {
			return controller.status("Не удалось сохранить запрос. Он не отправлен CLI."), nil
		}
		// Keep the prompt bound to the current card until background voice
		// preparation replaces it with the recognized/processed result.
		controller.mu.Lock()
		controller.promptSessions[messageID] = activeSession
		controller.mu.Unlock()
		// Voice transcription/download can be slow.  Acknowledge custody first
		// and continue preparation off the update handler so the user sees the
		// prompt state immediately instead of waiting for recognition.
		voiceCtx := context.WithoutCancel(controller.rootContext)
		go controller.prepareAndEnqueueVoice(voiceCtx, update, activeSession, messageID, promptText)
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	} else {
		controller.ScheduleStandby()
		if err := controller.setPromptState(ctx, activeSession, messageID, promptText, "🙋‍♂"); err != nil {
			return controller.status("Не удалось сохранить запрос. Он не отправлен CLI."), nil
		}
	}
	prepared, rejection := controller.preparation.Prepare(ctx, update)
	if rejection != "" {
		controller.setPromptState(ctx, activeSession, messageID, promptText, "🙅‍♂")
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
	if strings.TrimSpace(prepared.Text) != "" {
		promptText = prepared.Text
		controller.setPromptState(ctx, activeSession, messageID, promptText, "🙋‍♂")
	}
	payload := controller.preparation.Payload(ctx, prepared.Text)
	return controller.enqueue(ctx, update.ID, update.SourceMessageID, prepared, payload), nil
}

func (controller *Controller) prepareAndEnqueueVoice(ctx context.Context, update coordinator.Update, sessionID domain.SessionID, messageID, promptText string) {
	prepared, rejection := controller.preparation.Prepare(ctx, update)
	if rejection != "" {
		controller.setPromptState(ctx, sessionID, messageID, promptText, "🙅‍♂")
		controller.notifyPromptCard(ctx, sessionID, messageID, "🙅‍♂")
		return
	}
	if strings.TrimSpace(prepared.Text) != "" {
		promptText = prepared.Text
		controller.setPromptState(ctx, sessionID, messageID, promptText, "🙋‍♂")
		// Voice preparation runs outside the Telegram update handler.  Persisted
		// prompt state alone is not enough: without an output event the carrier
		// remains stale until the user re-enters the session menu.  Publish the
		// recognized text immediately; durable input processing will publish the
		// later preprocessing/provider states in order.
		controller.notifyPromptCard(ctx, sessionID, messageID, "🙋‍♂")
	}
	payload := controller.preparation.Payload(ctx, prepared.Text)
	controller.enqueue(ctx, update.ID, update.SourceMessageID, prepared, payload)
}

func (controller *Controller) notifyPromptCard(ctx context.Context, sessionID domain.SessionID, messageID, emoji string) {
	if sessionID == "" || strings.TrimSpace(messageID) == "" || strings.TrimSpace(emoji) == "" {
		return
	}
	controller.notify(ctx, Notification{
		OperationID:    messageID + ":prompt-status:" + emoji + ":card",
		ConversationID: controller.ownerPrivateChatID,
		SessionID:      sessionID,
		Kind:           NotificationPromptStatus,
		Text:           emoji,
	})
}

func (controller *Controller) consumePreprocessingInstruction(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool, error) {
	controller.mu.Lock()
	pending := controller.preprocessingInstructionPending
	controller.mu.Unlock()
	if !pending {
		return coordinator.Decision{}, false, nil
	}
	if update.MediaKind != "" || update.Caption != "" || strings.TrimSpace(update.Text) == "" {
		return controller.status("Инструкция должна быть одним непустым текстовым сообщением."), true, nil
	}
	preferences, ok := controller.settings.(settingsport.PreprocessingPreferences)
	if !ok {
		return coordinator.Decision{}, true, errors.New("preprocessing settings are not configured")
	}
	if err := preferences.SetPreprocessingInstruction(ctx, strings.TrimSpace(update.Text)); err != nil {
		return coordinator.Decision{}, true, fmt.Errorf("save preprocessing instruction: %w", err)
	}
	controller.mu.Lock()
	controller.preprocessingInstructionPending = false
	controller.mu.Unlock()
	controller.preparation.Invalidate(controller.currentNodeID())
	return controller.status("Инструкция препроцессинга сохранена."), true, nil
}

func (controller *Controller) consumeNodeRename(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool, error) {
	controller.mu.Lock()
	pending := controller.nodeRenamePending
	controller.mu.Unlock()
	if !pending {
		return coordinator.Decision{}, false, nil
	}
	if update.MediaKind != "" || update.Caption != "" || strings.TrimSpace(update.Text) == "" {
		return controller.status("Имя ноды должно быть непустым текстовым сообщением."), true, nil
	}
	renamer, ok := controller.providerPreferences.(settingsport.NodeRenamer)
	if !ok {
		return coordinator.Decision{}, true, errors.New("переименование ноды не настроено")
	}
	if err := renamer.RenameNode(ctx, controller.currentNodeID(), strings.TrimSpace(update.Text)); err != nil {
		return controller.status("Не удалось переименовать ноду: " + err.Error()), true, nil
	}
	controller.mu.Lock()
	controller.nodeRenamePending = false
	controller.mu.Unlock()
	return controller.status("Имя ноды сохранено."), true, nil
}

func (controller *Controller) handleCallback(ctx context.Context, update coordinator.Update) (coordinator.Decision, error) {
	if update.CallbackQueryID == "" || update.SourceMessageID <= 0 {
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
	if carrier, ok := controller.uiState.(CardCarrierStore); ok {
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active == "" {
			if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
				active, _ = loader.LoadActiveSession(ctx)
			}
		}
		if active != "" {
			if err := carrier.SetCardCarrier(ctx, active, update.ConversationID, update.SourceMessageID); err != nil {
				return coordinator.Decision{}, fmt.Errorf("persist Telegram card carrier: %w", err)
			}
		}
	}
	callbackText := strings.TrimSpace(update.Text)
	if strings.HasPrefix(callbackText, "mm:node:") {
		choice, parseErr := strconv.Atoi(strings.TrimPrefix(callbackText, "mm:node:"))
		if parseErr != nil || choice < 1 {
			return withCallbackID(controller.status("Нода недоступна."), update), nil
		}
		if _, err := controller.selectNodeSemantic(ctx, choice); err != nil {
			return coordinator.Decision{}, err
		}
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	}
	if strings.HasPrefix(callbackText, "session:select:") || strings.HasPrefix(callbackText, "sw:") {
		id := strings.TrimPrefix(callbackText, "session:select:")
		if id == callbackText {
			id = strings.TrimPrefix(callbackText, "sw:")
		}
		decision, err := controller.use(ctx, domain.SessionID(id))
		return withCallbackID(decision, update), err
	}
	switch callbackText {
	case "new:codex", "new:claude":
		provider := domain.ProviderCodex
		if callbackText == "new:claude" {
			provider = domain.ProviderClaude
		}
		if _, err := controller.newSessionSurface(ctx); err != nil {
			return coordinator.Decision{}, err
		}
		if err := controller.selectCreateProvider(ctx, provider); err != nil {
			return withCallbackID(controller.status(unavailableNewSessionSurface().Text), update), nil
		}
		providers, err := controller.availableProviders(ctx)
		if err != nil {
			return coordinator.Decision{}, err
		}
		draft, err := controller.createFlow.Confirm(providers)
		if err != nil {
			return withCallbackID(controller.status("Не удалось подтвердить компьютер и рабочую папку."), update), nil
		}
		decision, err := controller.create(ctx, update.ID, draft.ComputerID, provider, draft.Workdir)
		if errors.Is(err, errProviderUnavailable) {
			return withCallbackID(controller.status(unavailableNewSessionSurface().Text), update), nil
		}
		return withCallbackID(decision, update), err
	case "mm:status":
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "mm:nodes":
		decision, err := controller.legacyNodeMenu(ctx)
		return withCallbackID(decision, update), err
	case "mm:list":
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "mm:new":
		return withCallbackID(controller.newMenu(ctx), update), nil
	case "mm:arch":
		return withCallbackID(controller.archiveMenu(ctx), update), nil
	case "mm:set":
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "mm:back", "ft:more":
		return withCallbackID(controller.menuStatus("Меню"), update), nil
	case "ft:stop":
		controller.stopCurrent(ctx)
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "pg:prev", "pg:next", "pg:jump":
		decision, err := controller.cardNavigation(ctx, callbackText)
		return withCallbackID(decision, update), err
	case "menu:status":
		return withCallbackID(controller.menuStatus("Bria работает и готова принимать команды."), update), nil
	case "menu:sessions":
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "menu:new":
		return withCallbackID(controller.newMenu(ctx), update), nil
	case "menu:archive":
		return withCallbackID(controller.menuStatus("Архив пока пуст."), update), nil
	case "menu:settings":
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "settings:screen":
		if controller.settings != nil {
			if err := controller.settings.ToggleScreen(ctx); err != nil {
				return coordinator.Decision{}, err
			}
		}
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "settings:detail":
		if controller.settings != nil {
			if err := controller.settings.ToggleCardDetail(ctx); err != nil {
				return coordinator.Decision{}, err
			}
		}
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "card:prev", "card:next", "card:latest":
		decision, err := controller.cardNavigation(ctx, update.Text)
		return withCallbackID(decision, update), err
	default:
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
}

func (controller *Controller) legacyNodeMenu(ctx context.Context) (coordinator.Decision, error) {
	nodes, err := controller.nodeInventory(ctx)
	if err != nil {
		return coordinator.Decision{}, err
	}
	view := telegramnodes.Menu(controller.currentNodeID(), nodes)
	keyboard := make(coordinator.KeyboardMarkup, 0, len(view)+1)
	for _, row := range view {
		keyboard = append(keyboard, []coordinator.KeyboardButton{{Text: row[0].Label, CallbackData: "mm:node:" + strconv.Itoa(row[0].Choice)}})
	}
	keyboard = append(keyboard, []coordinator.KeyboardButton{{Text: "Назад", CallbackData: "mm:back"}})
	decision := controller.status("Ноды")
	decision.Keyboard = &keyboard
	return decision, nil
}
func (controller *Controller) mainSurface(ctx context.Context) (coordinator.Decision, error) {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	if active == "" {
		if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
			active, _ = loader.LoadActiveSession(ctx)
		}
	}
	if active != "" {
		return controller.cardDecision(ctx, active, "")
	}
	return controller.menuStatus("Bria готова. Нет активной сессии. Выберите «Новая» в меню."), nil
}
func (controller *Controller) cardNavigation(ctx context.Context, action string) (coordinator.Decision, error) {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	if active == "" {
		if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
			active, _ = loader.LoadActiveSession(ctx)
		}
	}
	if active == "" {
		return controller.menuStatus("Нет активной сессии."), nil
	}
	return controller.cardDecision(ctx, active, action)
}
func (controller *Controller) cardDecision(ctx context.Context, sessionID domain.SessionID, action string) (coordinator.Decision, error) {
	session, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return controller.status("Сессия " + string(sessionID) + " готова."), nil
	}
	controller.mu.Lock()
	items := append([]string(nil), controller.history[sessionID]...)
	controller.mu.Unlock()
	if len(items) == 0 {
		if historyStore, ok := controller.uiState.(CardHistoryStore); ok {
			items, _ = historyStore.LoadCardHistory(ctx, sessionID)
		}
	}
	blocks, err := controller.displayHistory(ctx, sessionID, items)
	if err != nil {
		return coordinator.Decision{}, err
	}
	pageLimit, err := controller.cardPageLimit(ctx)
	if err != nil {
		return coordinator.Decision{}, err
	}
	pages := cardtranscript.Paginate(blocks, pageLimit)
	controller.mu.Lock()
	fallback := cardpageselection.View{Page: controller.page[sessionID], Pages: max(controller.page[sessionID], len(pages)), Anchor: controller.pageAnchor[sessionID], FollowLatest: controller.followLatest[sessionID]}
	controller.mu.Unlock()
	view, err := cardpageselection.Select(ctx, controller.uiState, sessionID, fallback, pages, action)
	if err != nil {
		return coordinator.Decision{}, err
	}
	if pager, ok := controller.uiState.(CardPageStore); ok {
		if err := pager.SetCardPage(ctx, sessionID, view.Page, view.Pages, view.Anchor, view.FollowLatest); err != nil {
			return coordinator.Decision{}, err
		}
	}
	controller.mu.Lock()
	controller.page[sessionID], controller.followLatest[sessionID], controller.pageAnchor[sessionID] = view.Page, view.FollowLatest, view.Anchor
	controller.mu.Unlock()
	page := view.Page
	stateText := string(session.Status())
	if session.Status() == domain.SessionReady {
		stateText = "готова"
	}
	if session.Status() == domain.SessionAwaitingRecovery {
		stateText = "ожидает восстановления"
	}
	controller.mu.Lock()
	usable := false
	if live, ok := controller.live[sessionID]; ok {
		usable = live.Equal(session)
	}
	controller.mu.Unlock()
	if session.Status() == domain.SessionReady && !usable {
		stateText += " (процесс не запущен)"
	}
	header := fmt.Sprintf("Сессия %s\n%s %s\nРабочая папка: %s\nСтраница %d/%d\n\n", session.ID(), session.Provider(), stateText, session.Workdir(), page, len(pages))
	keyboard := coordinator.KeyboardMarkup{{{Text: "‹", CallbackData: "pg:prev"}, {Text: fmt.Sprintf("%d/%d", page, len(pages)), CallbackData: "pg:jump"}, {Text: "›", CallbackData: "pg:next"}}, {{Text: "Стоп", CallbackData: "ft:stop"}, {Text: "Опции", CallbackData: "ft:more"}}}
	if sessions, e := controller.sessions.List(ctx); e == nil {
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
		labelsByID := telegramsessions.Labels(sessions, session.ComputerID())
		row := []coordinator.KeyboardButton{}
		for _, candidate := range sessions {
			if candidate.ComputerID() != session.ComputerID() || candidate.Status() == domain.SessionArchived {
				continue
			}
			label := labelsByID[candidate.ID()]
			if candidate.ID() == sessionID {
				label = "✓ " + label
			}
			row = append(row, coordinator.KeyboardButton{Text: label, CallbackData: "sw:" + string(candidate.ID())})
			if len(row) == 3 {
				keyboard = append(keyboard, row)
				row = []coordinator.KeyboardButton{}
			}
		}
		if len(row) > 0 {
			keyboard = append(keyboard, row)
		}
	}
	keyboard = append(keyboard, []coordinator.KeyboardButton{
		{Text: "≡ Меню", CallbackData: "ft:more"},
		{Text: "Ноды", CallbackData: "mm:nodes"},
		{Text: "➕ Новая", CallbackData: "mm:new"},
	})
	return coordinator.Decision{Kind: coordinator.DecisionStatus, Status: coordinator.Status{ConversationID: controller.ownerPrivateChatID, Text: header + pages[page-1].Content}, Keyboard: &keyboard}, nil
}
func (controller *Controller) semanticCard(ctx context.Context, sessionID domain.SessionID, makeActive bool) (SemanticCard, error) {
	session, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return SemanticCard{}, fmt.Errorf("load semantic card session: %w", err)
	}
	controller.mu.Lock()
	items := append([]string(nil), controller.history[sessionID]...)
	page := controller.page[sessionID]
	followLatest := controller.followLatest[sessionID]
	anchor := controller.pageAnchor[sessionID]
	optionsExpanded := controller.optionsExpanded[sessionID]
	closeConfirmation := controller.closeConfirmation[sessionID]
	recoveryBusy := controller.recovering[sessionID]
	controller.mu.Unlock()
	if len(items) == 0 {
		if historyStore, ok := controller.uiState.(CardHistoryStore); ok {
			items, _ = historyStore.LoadCardHistory(ctx, sessionID)
		}
	}
	blocks, err := controller.displayHistory(ctx, sessionID, items)
	if err != nil {
		return SemanticCard{}, err
	}
	pageLimit, err := controller.cardPageLimit(ctx)
	if err != nil {
		return SemanticCard{}, err
	}
	pages := cardtranscript.Paginate(blocks, pageLimit)
	view, err := cardpageselection.Select(ctx, controller.uiState, sessionID, cardpageselection.View{Page: page, Pages: max(page, len(pages)), Anchor: anchor, FollowLatest: followLatest}, pages, "")
	if err != nil {
		return SemanticCard{}, err
	}
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return SemanticCard{}, fmt.Errorf("list semantic card sessions: %w", err)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	selectable := make([]domain.SessionID, 0, len(sessions))
	labelsByID := telegramsessions.Labels(sessions, session.ComputerID())
	for _, candidate := range sessions {
		if label := controller.standbyLabel(ctx, candidate); label != "" {
			labelsByID[candidate.ID()] = label
		}
	}
	selectableLabels := make([]string, 0, len(sessions))
	background := make([]string, 0, 5)
	for _, candidate := range sessions {
		if candidate.ComputerID() != session.ComputerID() || !telegramsessions.Viewable(candidate.Status()) {
			continue
		}
		selectable = append(selectable, candidate.ID())
		label := labelsByID[candidate.ID()]
		if candidate.ID() == sessionID {
			label = "✓ " + label
		} else if len(background) < 5 {
			background = append(background, fmt.Sprintf("%s %s · %s", sessionStatusGlyph(candidate.Status()), label, sessionStateText(candidate.Status())))
		}
		selectableLabels = append(selectableLabels, label)
	}
	rowSizes := make([]int, 0, (len(selectable)+2)/3)
	for remaining := len(selectable); remaining > 0; remaining -= 3 {
		size := remaining
		if size > 3 {
			size = 3
		}
		rowSizes = append(rowSizes, size)
	}
	working := session.Status() == domain.SessionRunning || session.Status() == domain.SessionStopping || session.Status() == domain.SessionClosingAfterWork
	stateText := sessionStateText(session.Status())
	controller.hydrateNativeModel(ctx, session)
	controller.mu.Lock()
	nativeModel := controller.nativeSnapshots[sessionID].Model
	controller.mu.Unlock()
	if nativeModel != "" {
		stateText += " · " + nativeModel
	}
	nodeName := string(session.ComputerID())
	if nodes, inventoryErr := controller.nodeInventory(ctx); inventoryErr == nil {
		for _, node := range nodes {
			if node.ID == session.ComputerID() && node.Name != "" {
				nodeName = node.Name
				break
			}
		}
	}
	footer := ""
	if len(background) > 0 {
		footer = "\n\n\u00a0\n\n─── фон ───  \n" + strings.Join(background, "  \n")
	}
	card := SemanticCard{
		SessionID: sessionID,
		Effect:    SemanticEditSameCarrier,
		Header:    fmt.Sprintf("%s · %s · %s · %s\n\n─────  \n", labelsByID[sessionID], nodeName, session.Provider(), stateText),
		Footer:    footer,
		Pages:     pages,
		View:      SemanticPageView{Page: view.Page, Pages: view.Pages, Anchor: view.Anchor, FollowLatest: view.FollowLatest},
		Working:   working, Archived: session.Status() == domain.SessionArchived,
		Recovery:        session.Status() == domain.SessionAwaitingRecovery,
		OptionsExpanded: optionsExpanded, SelectableSessionIDs: selectable,
		SelectableSessionLabels: selectableLabels, SessionRowSizes: rowSizes, MakeActive: makeActive,
	}
	card.Header += telegramsessionview.RecoveryNotice(session, recoveryBusy, controller.recoverer != nil)
	if closeConfirmation {
		label := labelsByID[sessionID]
		if label == "" {
			label = telegramsessions.ShortID(sessionID)
		}
		card.Header = "⚠️ Архивировать сессию " + label + "?\nСессия будет остановлена и перемещена в архив."
		if reader, ok := controller.sessions.(interface {
			HasEmptyCloseEligibility(context.Context, domain.SessionID) (bool, error)
		}); ok {
			empty, err := reader.HasEmptyCloseEligibility(ctx, sessionID)
			if err != nil {
				return SemanticCard{}, err
			}
			if empty {
				card.Header = "⚠️ Удалить пустую сессию " + label + "?\nВ ней нет запросов. Сессия будет остановлена и удалена без архивации."
				card.DeleteConfirmation = true
			}
		}
		card.Footer = ""
		card.Pages = []SemanticContentPage{{Content: "", Anchors: []string{"close-confirmation"}}}
		card.View = SemanticPageView{Page: 1, Pages: 1, Anchor: "close-confirmation", FollowLatest: true}
		card.OptionsExpanded = false
		card.SelectableSessionIDs = nil
		card.SelectableSessionLabels = nil
		card.SessionRowSizes = nil
		card.CloseConfirmation = true
		card.MakeActive = true
	}
	return card, nil
}

func sessionStateText(status domain.SessionStatus) string {
	return telegramsessionview.StateText(status)
}
func sessionStatusGlyph(status domain.SessionStatus) string {
	return telegramsessionview.StatusGlyph(status)
}

func (controller *Controller) availableSessionName(ctx context.Context, computerID domain.ComputerID, workdir string) (string, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return "", fmt.Errorf("list sessions for display name: %w", err)
	}
	return telegramsessions.AvailableName(sessions, computerID, workdir)
}
func (controller *Controller) cardPageLimit(ctx context.Context) (int, error) {
	const defaultLimit = 64
	if controller.settings == nil {
		return defaultLimit, nil
	}
	snapshot, err := controller.settings.Snapshot(ctx)
	if err != nil {
		return 0, fmt.Errorf("load card page limit: %w", err)
	}
	switch snapshot.CardPageLimit {
	case 32, 64, 128:
		return snapshot.CardPageLimit, nil
	default:
		return 0, errors.New("card page limit must be 32, 64, or 128")
	}
}

func (controller *Controller) settingsStatus(ctx context.Context) (coordinator.Decision, error) {
	result, err := controller.settingsSemanticResult(ctx)
	if err != nil {
		return coordinator.Decision{}, err
	}
	decision := controller.menuStatus(result.Surface.Text)
	decision.Status.RichMarkdown = result.Surface.RichMarkdown
	keyboard := coordinator.KeyboardMarkup{{{Text: "Screen", CallbackData: "settings:screen"}, {Text: "Детализация", CallbackData: "settings:detail"}}}
	keyboard = append(keyboard, (*decision.Keyboard)...)
	decision.Keyboard = &keyboard
	return decision, nil
}
func withCallbackID(decision coordinator.Decision, update coordinator.Update) coordinator.Decision {
	if decision.Kind == coordinator.DecisionStatus {
		decision.Status.CallbackQueryID = update.CallbackQueryID
		decision.Status.SourceMessageID = update.SourceMessageID
	}
	return decision
}
func (controller *Controller) menuStatus(text string) coordinator.Decision {
	decision := controller.status(text)
	keyboard := coordinator.KeyboardMarkup{
		{{Text: "Сессии", CallbackData: "menu:sessions"}, {Text: "Архив", CallbackData: "menu:archive"}},
		{{Text: "Ноды", CallbackData: "menu:status"}, {Text: "➕ Новая", CallbackData: "menu:new"}},
		{{Text: "Настройки", CallbackData: "menu:settings"}},
	}
	decision.Keyboard = &keyboard
	return decision
}
func (controller *Controller) newMenu(ctx context.Context) coordinator.Decision {
	providers, err := controller.availableProviders(ctx)
	if err != nil || len(providers) == 0 {
		return controller.menuStatus(unavailableNewSessionSurface().Text)
	}
	decision := controller.menuStatus("Новая сессия\nВыберите CLI (рабочая папка по умолчанию: /tmp):")
	buttons := make([]coordinator.KeyboardButton, 0, len(providers))
	for _, provider := range providers {
		buttons = append(buttons, coordinator.KeyboardButton{Text: authorizationProviderName(provider), CallbackData: "new:" + string(provider)})
	}
	keyboard := coordinator.KeyboardMarkup{buttons, {{Text: "≡ Меню", CallbackData: "mm:back"}}}
	decision.Keyboard = &keyboard
	return decision
}
func (controller *Controller) archiveMenu(ctx context.Context) coordinator.Decision {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return controller.status("Не удалось прочитать архив.")
	}
	var b strings.Builder
	b.WriteString("Архив\n")
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	count := 0
	currentNode := controller.currentNodeID()
	labelsByID := telegramsessions.Labels(sessions, currentNode)
	for _, s := range sessions {
		if s.ComputerID() == currentNode && s.Status() == domain.SessionArchived {
			fmt.Fprintf(&b, "\n%s %s %s", s.Provider(), labelsByID[s.ID()], s.Workdir())
			count++
		}
	}
	if count == 0 {
		b.WriteString("\nПусто.")
	}
	return controller.menuStatus(b.String())
}
func (controller *Controller) ResumeArchived(ctx context.Context, sessionID domain.SessionID) (coordinator.Decision, error) {
	if controller.asyncResumer != nil {
		return controller.resumeArchivedAsync(ctx, sessionID)
	}
	archived, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("load archived session before resume: %w", err)
	}
	if archived.ComputerID() != controller.currentNodeID() {
		return coordinator.Decision{}, errors.New("archived session does not belong to the selected node")
	}
	if controller.archivedResumer == nil {
		return coordinator.Decision{}, errors.New("archived session resumer is not configured")
	}
	prior, ok := archived.Binding()
	if archived.Status() != domain.SessionArchived || !ok {
		return coordinator.Decision{}, errors.New("semantic resume target is not an archived provider session")
	}
	resumed, err := controller.archivedResumer.Resume(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("resume archived session: %w", err)
	}
	binding, ok := resumed.Binding()
	if !ok || resumed.ID() != archived.ID() || resumed.Provider() != archived.Provider() ||
		resumed.ComputerID() != archived.ComputerID() || resumed.Workdir() != archived.Workdir() ||
		resumed.Status() != domain.SessionReady || binding.Provider != prior.Provider ||
		binding.SessionID != prior.SessionID || binding.Generation <= prior.Generation {
		return coordinator.Decision{}, errors.New("archived resumer returned an inconsistent exact continuation")
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.live[resumed.ID()] = resumed
	controller.active = resumed.ID()
	controller.ensureWorkerLocked(resumed.ID())
	priorCopy := prior
	controller.created[resumed.ID()] = createdProcess{
		request: app.StartSessionRequest{
			SessionID: resumed.ID(), ComputerID: resumed.ComputerID(), Provider: resumed.Provider(),
			Workdir: resumed.Workdir(), Mode: app.SessionStartResume, PriorBinding: &priorCopy,
		},
		binding: binding,
	}
	controller.mu.Unlock()
	if err := controller.persistActive(ctx, resumed.ID()); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist resumed active session: %w", err)
	}
	telegramturnhelpers.WakeReadyInput(controller.durableInput, resumed)
	return controller.cardDecision(ctx, resumed.ID(), "")
}

// CloseSession applies an already authenticated semantic close action. Busy
// sessions are durably scheduled by SessionCloser and are finalized by the
// worker after the provider terminal.
func (controller *Controller) CloseSession(ctx context.Context, sessionID domain.SessionID) (coordinator.Decision, error) {
	if controller.sessionCloser == nil {
		return coordinator.Decision{}, errors.New("session closer is not configured")
	}
	controller.mu.Lock()
	worker := controller.workers[sessionID]
	controller.mu.Unlock()
	if worker != nil && worker.hasActiveTurn() && controller.turnLifecycle == nil {
		return controller.status("Нельзя безопасно закрыть выполняющуюся сессию без учёта её состояния."), nil
	}
	return controller.closeSessionResult(ctx, sessionID, controller.sessionCloser.Close)
}

func (controller *Controller) closeSessionResult(ctx context.Context, sessionID domain.SessionID, closeFn func(context.Context, domain.SessionID) (app.CloseSessionResult, error)) (coordinator.Decision, error) {
	result, err := controller.closeFlow.Close(ctx, sessionID, closeFn)
	controller.closeFlow.Outcome(ctx, sessionID, result, err, controllertelemetry.ImmediateClose)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("close session: %w", err)
	}
	if result.Session.ID() != sessionID {
		return coordinator.Decision{}, errors.New("session closer returned an inconsistent session")
	}
	if result.Scheduled {
		if result.Session.Status() != domain.SessionClosingAfterWork && result.Session.Status() != domain.SessionClosing {
			return coordinator.Decision{}, errors.New("scheduled close did not persist a closing state")
		}
		controller.replaceLive(result.Session)
		if _, _, err := controller.selectAfterClose(ctx, result.Session); err != nil {
			return coordinator.Decision{}, err
		}
		if result.Session.Status() == domain.SessionClosing {
			return controller.status("Сессия закрывается…"), nil
		}
		return controller.status("Сессия будет закрыта после текущего запроса."), nil
	}
	if !result.Deleted && result.Session.Status() != domain.SessionArchived {
		return coordinator.Decision{}, errors.New("confirmed close did not archive the session")
	}
	if err := controller.applyClosedSession(ctx, result.Session); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist closed node session: %w", err)
	}
	return controller.archiveMenu(ctx), nil
}
func (controller *Controller) applyClosedSession(ctx context.Context, session domain.Session) error {
	fallback, changed, err := controller.selectAfterClose(ctx, session)
	if err != nil {
		return err
	}
	if changed && fallback != "" {
		controller.notify(context.WithoutCancel(ctx), Notification{
			OperationID:    "session-close:" + string(session.ID()) + ":active:" + string(fallback),
			ConversationID: controller.ownerPrivateChatID,
			SessionID:      fallback,
			Kind:           NotificationPromptStatus,
			Text:           "session-closed",
		})
	}
	controller.ScheduleStandby()
	return nil
}
func (controller *Controller) status(text string) coordinator.Decision {
	return coordinator.Decision{
		Kind: coordinator.DecisionStatus,
		Status: coordinator.Status{
			ConversationID: controller.ownerPrivateChatID,
			Text:           text,
		},
	}
}

// DeliveryFailure returns the latest unconfirmed notification delivery for a
// session. The state is observational only and never triggers an automatic
// retry.
func (controller *Controller) DeliveryFailure(sessionID domain.SessionID) (NotificationFailure, bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	failure, ok := controller.deliveryFailures[sessionID]
	return failure, ok
}

// notify confirms exact durable custody, or one direct transport attempt.
// Final completion requires custody success; ambiguous enqueue is reconciled
// by exact operation identity, never by replaying the provider request.
func (controller *Controller) notify(ctx context.Context, notification Notification) bool {
	if controller.durableOutput != nil {
		if notification.OperationID == "" {
			controller.recordNotificationFailure(ctx, notification, false)
			return false
		}
		receipt, err := controller.durableOutput.AcceptOutput(ctx, OutgoingNotification{
			OperationID: notification.OperationID, ConversationID: notification.ConversationID,
			SessionID: notification.SessionID, Kind: notification.Kind, Payload: []byte(notification.Text),
		})
		if err != nil || receipt.SessionID != notification.SessionID ||
			receipt.OperationID != notification.OperationID || receipt.Sequence == 0 {
			controller.recordNotificationFailure(ctx, notification, false)
			return false
		}
		return true
	}
	if err := controller.notifier.Notify(ctx, notification); err == nil {
		return true
	}
	controller.recordNotificationFailure(ctx, notification, true)
	return false
}
func (controller *Controller) recordNotificationFailure(ctx context.Context, notification Notification, attempted bool) {
	failure := NotificationFailure{
		SessionID: notification.SessionID,
		Kind:      notification.Kind,
		State:     DeliveryUnknown,
	}
	if attempted && controller.outputFailures != nil {
		failure.DurablyRecorded = controller.outputFailures.RecordNotificationFailure(
			context.WithoutCancel(ctx),
			failure,
		) == nil
	}
	controller.mu.Lock()
	controller.deliveryFailures[notification.SessionID] = failure
	controller.mu.Unlock()
}
func (controller *Controller) createAsync(
	ctx context.Context,
	updateID int64,
	computerID domain.ComputerID,
	provider domain.Provider,
	workdir string,
	name string,
) (coordinator.Decision, error) {
	intent := app.ConfirmedSessionIntent{
		IntentID:   domain.IntentID("telegram-update:" + strconv.FormatInt(updateID, 10)),
		ComputerID: computerID,
		Provider:   provider,
		Workdir:    workdir,
		Name:       name,
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.creates.Add(1)
	previousActive := controller.nodes.Active(computerID)
	controller.mu.Unlock()
	beginContext, cancelBegin := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(controller.rootContext, cancelBegin)
	pending, err := controller.asyncCreator.BeginCreate(beginContext, intent)
	stopCancellation()
	cancelBegin()
	if err != nil {
		controller.creates.Done()
		return coordinator.Decision{}, fmt.Errorf("begin confirmed session creation: %w", err)
	}
	if err := validatePendingCreate(pending, intent); err != nil {
		controller.creates.Done()
		return coordinator.Decision{}, err
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		controller.creates.Done()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.pending[pending.Session.ID()] = pending.Session
	controller.active = pending.Session.ID()
	controller.mu.Unlock()
	go controller.awaitCreatedSession(intent, pending, previousActive)
	if err := controller.persistActive(ctx, pending.Session.ID()); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist starting active Telegram session: %w", err)
	}
	return controller.cardDecision(ctx, pending.Session.ID(), "")
}
func validatePendingCreate(pending PendingSessionStart, intent app.ConfirmedSessionIntent) error {
	session := pending.Session
	if pending.Outcome == nil {
		return errors.New("async session creator returned no terminal outcome")
	}
	if session.ID() == "" || session.IntentID() != intent.IntentID ||
		session.ComputerID() != intent.ComputerID || session.Provider() != intent.Provider ||
		session.Workdir() != intent.Workdir || session.Status() != domain.SessionStarting {
		return errors.New("async session creator returned an inconsistent durable starting session")
	}
	if _, hasBinding := session.Binding(); hasBinding {
		return errors.New("durable starting session unexpectedly has a provider binding")
	}
	return nil
}
func (controller *Controller) awaitCreatedSession(intent app.ConfirmedSessionIntent, pending PendingSessionStart, previousActive domain.SessionID) {
	defer controller.creates.Done()
	select {
	case <-controller.rootContext.Done():
		return
	case outcome, ok := <-pending.Outcome:
		if !ok {
			controller.asyncStartFailed(pending.Session.ID(), "Запуск сессии завершился без подтверждённого результата.", previousActive)
			return
		}
		if outcome.Err != nil || !sameSessionIdentity(outcome.Session, pending.Session) {
			controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", previousActive)
			return
		}
		binding, hasBinding := outcome.Session.Binding()
		switch outcome.Session.Status() {
		case domain.SessionReady:
			if !hasBinding || outcome.StartError != nil {
				controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", previousActive)
				return
			}
			controller.mu.Lock()
			delete(controller.pending, outcome.Session.ID())
			controller.live[outcome.Session.ID()] = outcome.Session
			controller.ensureWorkerLocked(outcome.Session.ID())
			if !outcome.Replayed {
				controller.created[outcome.Session.ID()] = createdProcess{
					request: requestFromSession(outcome.Session),
					binding: binding,
				}
			}
			controller.mu.Unlock()
		case domain.SessionAwaitingRecovery:
			if hasBinding || outcome.Replayed || outcome.StartError == nil {
				controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", previousActive)
				return
			}
			controller.mu.Lock()
			controller.pending[outcome.Session.ID()] = outcome.Session
			controller.mu.Unlock()
		default:
			controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", previousActive)
			return
		}
		controller.notify(context.WithoutCancel(controller.rootContext), Notification{
			OperationID:    "session-start:" + string(outcome.Session.ID()) + ":state",
			ConversationID: controller.ownerPrivateChatID,
			SessionID:      outcome.Session.ID(),
			Kind:           NotificationPromptStatus,
			Text:           "session-state",
		})
	}
}
func (controller *Controller) resumeArchivedAsync(ctx context.Context, sessionID domain.SessionID) (coordinator.Decision, error) {
	archived, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("load archived session before resume: %w", err)
	}
	if archived.ComputerID() != controller.currentNodeID() {
		return coordinator.Decision{}, errors.New("archived session does not belong to the selected node")
	}
	prior, ok := archived.Binding()
	if archived.Status() != domain.SessionArchived || !ok {
		return coordinator.Decision{}, errors.New("semantic resume target is not an archived provider session")
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	previousActive := controller.active
	controller.creates.Add(1)
	controller.mu.Unlock()
	beginContext, cancelBegin := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(controller.rootContext, cancelBegin)
	pending, err := controller.asyncResumer.BeginResume(beginContext, sessionID)
	stopCancellation()
	cancelBegin()
	if err != nil {
		controller.creates.Done()
		return coordinator.Decision{}, fmt.Errorf("begin exact archive resume: %w", err)
	}
	if pending.Outcome == nil || !sameSessionIdentity(pending.Session, archived) || pending.Session.Status() != domain.SessionResuming {
		controller.creates.Done()
		return coordinator.Decision{}, errors.New("async archived resumer returned an inconsistent durable resuming session")
	}
	pendingBinding, hasBinding := pending.Session.Binding()
	if !hasBinding || pendingBinding != prior {
		controller.creates.Done()
		return coordinator.Decision{}, errors.New("durable resuming session lost the original provider binding")
	}
	controller.mu.Lock()
	controller.pending[sessionID] = pending.Session
	controller.active = sessionID
	controller.mu.Unlock()
	go controller.awaitResumedSession(archived, prior, previousActive, pending)
	if err := controller.persistActive(ctx, sessionID); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist resuming active Telegram session: %w", err)
	}
	return controller.cardDecision(ctx, sessionID, "")
}
func (controller *Controller) awaitResumedSession(
	archived domain.Session,
	prior domain.ProviderBinding,
	previousActive domain.SessionID,
	pending PendingSessionStart,
) {
	defer controller.creates.Done()
	select {
	case <-controller.rootContext.Done():
		return
	case outcome, ok := <-pending.Outcome:
		if !ok || outcome.Err != nil || !sameSessionIdentity(outcome.Session, archived) {
			controller.asyncStartFailed(archived.ID(), "Не удалось продолжить исходную сессию.", previousActive)
			return
		}
		binding, hasBinding := outcome.Session.Binding()
		if outcome.Session.Status() != domain.SessionReady || !hasBinding ||
			binding.Provider != prior.Provider || binding.SessionID != prior.SessionID ||
			binding.Generation <= prior.Generation || outcome.StartError != nil {
			controller.asyncStartFailed(archived.ID(), "Не удалось продолжить исходную сессию.", previousActive)
			return
		}
		priorCopy := prior
		controller.mu.Lock()
		delete(controller.pending, outcome.Session.ID())
		controller.live[outcome.Session.ID()] = outcome.Session
		controller.ensureWorkerLocked(outcome.Session.ID())
		controller.created[outcome.Session.ID()] = createdProcess{
			request: app.StartSessionRequest{
				SessionID: outcome.Session.ID(), ComputerID: outcome.Session.ComputerID(), Provider: outcome.Session.Provider(),
				Workdir: outcome.Session.Workdir(), Mode: app.SessionStartResume, PriorBinding: &priorCopy,
			},
			binding: binding,
		}
		controller.mu.Unlock()
		telegramturnhelpers.WakeReadyInput(controller.durableInput, outcome.Session)
	}
}
func (controller *Controller) asyncStartFailed(sessionID domain.SessionID, text string, previousActive domain.SessionID) {
	controller.mu.Lock()
	failed := controller.pending[sessionID]
	delete(controller.pending, sessionID)
	if controller.active == sessionID {
		controller.active = previousActive
	}
	nodeID := failed.ComputerID()
	controller.mu.Unlock()
	if nodeID != "" {
		ctx := context.WithoutCancel(controller.rootContext)
		_ = controller.nodes.RestoreActive(ctx, nodeID, previousActive)
	}
	controller.notify(context.WithoutCancel(controller.rootContext), Notification{
		OperationID:    "session-start:" + string(sessionID),
		ConversationID: controller.ownerPrivateChatID,
		SessionID:      sessionID,
		Kind:           NotificationError,
		Text:           text,
	})
}
func sameSessionIdentity(left, right domain.Session) bool {
	return left.ID() != "" && left.ID() == right.ID() && left.IntentID() == right.IntentID() &&
		left.ComputerID() == right.ComputerID() && left.Provider() == right.Provider() &&
		left.Workdir() == right.Workdir()
}
func (controller *Controller) create(
	ctx context.Context,
	updateID int64,
	computerID domain.ComputerID,
	provider domain.Provider,
	workdir string,
) (coordinator.Decision, error) {
	if updateID <= 0 {
		return coordinator.Decision{}, errors.New("confirmed creation update id must be positive")
	}
	enabled, err := controller.providerEnabled(ctx, computerID, provider)
	if err != nil {
		return coordinator.Decision{}, err
	}
	if !enabled {
		return coordinator.Decision{}, errProviderUnavailable
	}
	name, err := controller.availableSessionName(ctx, computerID, workdir)
	if err != nil {
		return coordinator.Decision{}, err
	}
	if controller.asyncCreator != nil {
		return controller.createAsync(ctx, updateID, computerID, provider, workdir, name)
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.creates.Add(1)
	controller.mu.Unlock()
	defer controller.creates.Done()
	createContext, cancelCreate := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(controller.rootContext, cancelCreate)
	defer func() {
		stopCancellation()
		cancelCreate()
	}()
	intent := app.ConfirmedSessionIntent{
		IntentID:   domain.IntentID("telegram-update:" + strconv.FormatInt(updateID, 10)),
		ComputerID: computerID,
		Provider:   provider,
		Workdir:    workdir,
		Name:       name,
	}
	result, err := controller.creator.Create(createContext, intent)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("create confirmed session: %w", err)
	}
	if result.Session.ID() == "" || result.Session.IntentID() != intent.IntentID ||
		result.Session.ComputerID() != computerID ||
		result.Session.Provider() != provider || result.Session.Workdir() != workdir {
		return coordinator.Decision{}, errors.New("session creator returned an inconsistent session")
	}
	binding, hasBinding := result.Session.Binding()
	if result.Replayed && result.StartError != nil {
		return coordinator.Decision{}, errors.New("replayed session contains a new start error")
	}
	switch result.Session.Status() {
	case domain.SessionAwaitingRecovery:
		if hasBinding || !result.Replayed && result.StartError == nil {
			return coordinator.Decision{}, errors.New("session creator returned inconsistent recovery state")
		}
	case domain.SessionReady:
		if !hasBinding || result.StartError != nil {
			return coordinator.Decision{}, errors.New("session creator returned inconsistent ready state")
		}
	default:
		return coordinator.Decision{}, errors.New("session creator returned a non-terminal creation state")
	}
	controller.mu.Lock()
	controller.active = result.Session.ID()
	if !result.Replayed && result.StartError == nil &&
		result.Session.Status() == domain.SessionReady && hasBinding {
		controller.live[result.Session.ID()] = result.Session
		controller.ensureWorkerLocked(result.Session.ID())
		controller.created[result.Session.ID()] = createdProcess{
			request: requestFromSession(result.Session),
			binding: binding,
		}
	}
	_, usable := controller.usableLocked(result.Session)
	controller.mu.Unlock()
	if err := controller.persistActive(ctx, result.Session.ID()); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist active Telegram session: %w", err)
	}
	switch result.Session.Status() {
	case domain.SessionAwaitingRecovery:
		return controller.cardDecision(ctx, result.Session.ID(), "")
	case domain.SessionReady:
		if !usable {
			return controller.cardDecision(ctx, result.Session.ID(), "")
		}
		return controller.cardDecision(ctx, result.Session.ID(), "")
	}
	return coordinator.Decision{}, errors.New("validated session status changed unexpectedly")
}
func (controller *Controller) persistActive(ctx context.Context, sessionID domain.SessionID) error {
	controller.mu.Lock()
	session, ok := controller.live[sessionID]
	if !ok {
		session, ok = controller.pending[sessionID]
	}
	controller.mu.Unlock()
	if !ok {
		var err error
		session, err = controller.sessions.Load(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("load active session for node scope: %w", err)
		}
	}
	return controller.setCurrentNodeActive(ctx, session)
}
func (controller *Controller) use(
	ctx context.Context,
	sessionID domain.SessionID,
) (coordinator.Decision, error) {
	session, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("load selected session: %w", err)
	}
	if session.ComputerID() != controller.currentNodeID() {
		return coordinator.Decision{}, errors.New("session does not belong to the selected node")
	}
	controller.mu.Lock()
	_, usable := controller.usableLocked(session)
	controller.mu.Unlock()
	if !usable && session.Status() != domain.SessionAwaitingRecovery {
		if session.Status() == domain.SessionReady {
			return controller.cardDecision(ctx, sessionID, "")
		}
		return controller.status("Сессия " + string(sessionID) + " недоступна: " + string(session.Status()) + "."), nil
	}
	if err := controller.closeFlow.Selection(ctx, session, controller.nodes, func() error { return controller.persistActive(ctx, sessionID) }); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist active Telegram session: %w", err)
	}
	return controller.cardDecision(ctx, sessionID, "")
}
func (controller *Controller) listSessions(ctx context.Context) (coordinator.Decision, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) == 0 {
		return controller.menuStatus("Сессий нет."), nil
	}
	sort.Slice(sessions, func(left, right int) bool {
		return sessions[left].ID() < sessions[right].ID()
	})
	controller.mu.Lock()
	active := controller.active
	live := make(map[domain.SessionID]domain.Session, len(controller.live))
	for id, session := range controller.live {
		live[id] = session
	}
	controller.mu.Unlock()
	var text strings.Builder
	text.WriteString("Сессии\nВыберите сессию:")
	currentNode := controller.currentNodeID()
	keyboard := coordinator.KeyboardMarkup{}
	row := []coordinator.KeyboardButton{}
	for _, session := range sessions {
		if session.ComputerID() != currentNode || session.Status() == domain.SessionArchived {
			continue
		}
		marker := ""
		if session.ID() == active {
			marker = "✓ "
		}
		label := marker + string(session.Provider()) + " " + telegramsessions.ShortID(session.ID())
		row = append(row, coordinator.KeyboardButton{Text: label, CallbackData: "sw:" + string(session.ID())})
		if len(row) == 3 {
			keyboard = append(keyboard, row)
			row = []coordinator.KeyboardButton{}
		}
		fmt.Fprintf(&text, "\n%s%s %s", marker, session.Provider(), session.Status())
		if session.Status() == domain.SessionReady {
			tracked, ok := live[session.ID()]
			if !ok || !tracked.Equal(session) {
				text.WriteString(" (процесс не запущен)")
			}
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}
	keyboard = append(keyboard, []coordinator.KeyboardButton{{Text: "+ Новая", CallbackData: "mm:new"}, {Text: "≡ Меню", CallbackData: "mm:back"}})
	decision := controller.status(text.String())
	decision.Keyboard = &keyboard
	return decision, nil
}
func (controller *Controller) enqueue(ctx context.Context, updateID, sourceMessageID int64, input PreparedInput, payload []byte) coordinator.Decision {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	return controller.enqueueSession(ctx, updateID, sourceMessageID, active, input, payload)
}
func (controller *Controller) enqueueSession(ctx context.Context, updateID, sourceMessageID int64, sessionID domain.SessionID, input PreparedInput, payload []byte) coordinator.Decision {
	messageID := "telegram-update:" + strconv.FormatInt(updateID, 10)
	if len(payload) == 0 {
		payload = []byte(input.Text)
	}
	controller.mu.Lock()
	worker := controller.workers[sessionID]
	session, live := controller.live[sessionID]
	_, usable := controller.usableLocked(session)
	pending, isPending := controller.pending[sessionID]
	closed := controller.closed
	controller.mu.Unlock()
	if closed {
		controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	}
	if controller.durableInput != nil {
		if sessionID == "" || (!live || !usable) && (!isPending || !acceptsDurableInput(pending.Status())) {
			controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
			return coordinator.Decision{Kind: coordinator.DecisionSkip}
		}
		if updateID <= 0 {
			controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
			return coordinator.Decision{Kind: coordinator.DecisionSkip}
		}
		if err := telegramturnhelpers.AcceptInput(ctx, controller.durableInput, sessionID, messageID, input, payload); err != nil {
			controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
			return coordinator.Decision{Kind: coordinator.DecisionSkip}
		}
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	}
	if len(input.Attachments) != 0 {
		controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	}
	if sessionID == "" || !live || !usable {
		controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	}
	if worker == nil {
		controller.setPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂")
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	}
	processed, preprocessingFailed, _ := controller.preparation.Process(ctx, session, messageID, payload)
	input.Text = processed
	if state, decodeErr := promptpreprocess.DecodeState(payload); decodeErr == nil && state.Enabled {
		controller.publishPreprocessingState(ctx, sessionID, messageID, input.Text, preprocessingFailed)
	}
	select {
	case worker.queue <- queuedTurn{text: input.Text, messageID: messageID}:
		controller.setProcessedPromptState(ctx, sessionID, messageID, input.Text, "👨‍💻", preprocessingFailed)
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	case <-controller.rootContext.Done():
		controller.setProcessedPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂", preprocessingFailed)
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	default:
		controller.setProcessedPromptState(ctx, sessionID, messageID, input.Text, "🙅‍♂", preprocessingFailed)
		return coordinator.Decision{Kind: coordinator.DecisionSkip}
	}
}

func (controller *Controller) setPromptState(ctx context.Context, sessionID domain.SessionID, messageID, text, emoji string) error {
	text = strings.TrimSpace(text)
	if sessionID == "" || strings.TrimSpace(messageID) == "" || text == "" {
		return nil
	}
	return controller.setPromptEntry(ctx, sessionID, messageID, emoji+" "+text)
}

func (controller *Controller) setProcessedPromptState(ctx context.Context, sessionID domain.SessionID, messageID, text, emoji string, preprocessingFailed bool) error {
	text = strings.TrimSpace(text)
	if !preprocessingFailed {
		return controller.setPromptState(ctx, sessionID, messageID, text, emoji)
	}
	return controller.setPromptEntry(ctx, sessionID, messageID, "❌ Ошибка препроцессинга\n"+emoji+" "+text)
}

func (controller *Controller) setPromptEntry(ctx context.Context, sessionID domain.SessionID, messageID, entry string) error {
	entry = strings.TrimSpace(entry)
	if sessionID == "" || strings.TrimSpace(messageID) == "" || entry == "" {
		return nil
	}
	if store, ok := controller.uiState.(CardPromptStore); ok {
		if err := store.SetCardPrompt(ctx, sessionID, messageID, entry); err != nil {
			return err
		} else {
			controller.ScheduleStandby()
			if historyStore, ok := controller.uiState.(CardHistoryStore); ok {
				if history, err := historyStore.LoadCardHistory(ctx, sessionID); err == nil {
					controller.mu.Lock()
					controller.history[sessionID] = history
					controller.promptSessions[messageID] = sessionID
					controller.mu.Unlock()
					return nil
				}
			}
		}
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	indexes := controller.promptIndexes[sessionID]
	if indexes == nil {
		indexes = make(map[string]int)
		controller.promptIndexes[sessionID] = indexes
	}
	if index, ok := indexes[messageID]; ok && index >= 0 && index < len(controller.history[sessionID]) {
		controller.history[sessionID][index] = entry
	} else {
		indexes[messageID] = len(controller.history[sessionID])
		controller.history[sessionID] = append(controller.history[sessionID], entry)
	}
	controller.promptSessions[messageID] = sessionID
	return nil
}

func (controller *Controller) publishPromptState(ctx context.Context, sessionID domain.SessionID, messageID, text, emoji string) error {
	return controller.publishProcessedPromptState(ctx, sessionID, messageID, text, emoji, false)
}

func (controller *Controller) publishPreprocessingState(ctx context.Context, sessionID domain.SessionID, messageID, text string, failed bool) {
	controller.setProcessedPromptState(ctx, sessionID, messageID, text, "🙋‍♂", failed)
	controller.notify(ctx, Notification{
		OperationID:    messageID + ":prompt-status:preprocessed",
		ConversationID: controller.ownerPrivateChatID,
		SessionID:      sessionID,
		Kind:           NotificationPromptStatus,
		Text:           "🙋‍♂",
	})
}

func (controller *Controller) publishProcessedPromptState(ctx context.Context, sessionID domain.SessionID, messageID, text, emoji string, preprocessingFailed bool) error {
	if err := controller.setProcessedPromptState(ctx, sessionID, messageID, text, emoji, preprocessingFailed); err != nil {
		return err
	}
	controller.mu.Lock()
	delete(controller.promptSessions, messageID)
	controller.mu.Unlock()
	controller.notify(ctx, Notification{
		OperationID:    messageID + ":prompt-status:" + emoji,
		ConversationID: controller.ownerPrivateChatID,
		SessionID:      sessionID,
		Kind:           NotificationPromptStatus,
		Text:           emoji,
	})
	return nil
}
func acceptsDurableInput(status domain.SessionStatus) bool {
	return status == domain.SessionStarting || status == domain.SessionResuming
}

// ProcessDurableInput processes one exact leased journal input through the
// same turn/lifecycle path as the in-memory worker. Early success needs custody;
// an exact provider ACK survives custody failure as awaiting recovery.
func (controller *Controller) ProcessDurableInput(
	ctx context.Context,
	input DurableLeasedInput,
	callbacks DurableInputCallbacks,
) (DurableInputProcessReceipt, error) {
	receipt := DurableInputProcessReceipt{
		SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
	}
	promptText, preprocessingEnabled, err := telegramturnhelpers.ValidateLeasedInput(input, callbacks)
	if err != nil {
		return receipt, err
	}
	if err := telegramturnhelpers.ValidateProvider(controller.submitter, controller.attachments, input); err != nil {
		controller.publishPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂")
		return receipt, err
	}
	controller.mu.Lock()
	worker := controller.workers[input.SessionID]
	session, usable := controller.usableLocked(controller.live[input.SessionID])
	closed := controller.closed
	if !closed {
		controller.durableWork.Add(1)
	}
	controller.mu.Unlock()
	if closed {
		controller.publishPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂")
		return receipt, errors.New("Telegram controller is closed")
	}
	defer controller.durableWork.Done()
	processContext, cancelProcess := context.WithCancel(ctx)
	stopRootCancellation := context.AfterFunc(controller.rootContext, cancelProcess)
	defer func() {
		stopRootCancellation()
		cancelProcess()
	}()
	ctx = processContext
	if worker == nil || !usable || session.ID() != input.SessionID {
		controller.publishPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂")
		return receipt, errors.New("durable input session is not live")
	}
	promptText, preprocessingFailed, preparedPayload := controller.preparation.Process(ctx, session, input.MessageID, input.Payload)
	if err := telegramturnhelpers.PersistPrepared(ctx, input, preparedPayload, callbacks); err != nil {
		controller.publishProcessedPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
		return receipt, err
	}
	if preprocessingEnabled {
		controller.publishPreprocessingState(ctx, input.SessionID, input.MessageID, promptText, preprocessingFailed)
	}
	binding, hasBinding := session.Binding()
	if len(input.Attachments) != 0 && (!hasBinding || strings.TrimSpace(binding.SessionID) == "") {
		controller.publishProcessedPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
		return receipt, errors.New("attachment session has no exact provider binding")
	}
	if terminal, ticket := worker.currentCompletion(); terminal != nil {
		steerer, ok := controller.submitter.(sessionruntime.CurrentTurnSubmitter)
		if !ok {
			ticket.Finish(nil)
			controller.publishProcessedPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
			return receipt, errors.New("provider does not support current-turn input")
		}
		if len(input.Attachments) != 0 {
			ticket.Finish(nil)
			controller.publishProcessedPromptState(ctx, input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
			return receipt, errors.New("current-turn attachments require turn-scoped custody")
		}
		accepted := false
		err := steerer.SubmitCurrentWithCallbacks(ctx, input.SessionID, sessionruntime.StructuredInput{Text: promptText}, sessionruntime.TurnCallbacks{
			MessageID: input.MessageID,
			OnAccepted: func(messageID string) error {
				if !worker.sameCompletion(terminal) {
					return errors.New("current-turn acceptance crossed a turn boundary")
				}
				if accepted || messageID != input.MessageID {
					return errors.New("provider returned invalid current-turn acceptance")
				}
				accepted = true
				acceptanceErr := callbacks.OnAccepted(ctx, DurableInputAcceptance{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence})
				controller.publishProcessedPromptState(context.WithoutCancel(ctx), input.SessionID, input.MessageID, promptText, "👨‍💻", preprocessingFailed)
				return acceptanceErr
			},
		})
		receipt.Accepted = accepted
		if accepted && err != nil {
			receipt.Completion = DurableInputAwaitingRecovery
			ticket.Finish(err)
			return receipt, err
		}
		if err != nil || !accepted {
			controller.publishProcessedPromptState(context.WithoutCancel(ctx), input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
			err = errors.Join(err, errors.New("provider did not accept current-turn input"))
			ticket.Finish(err)
			return receipt, err
		}
		receipt.Completion = DurableInputPending
		controller.awaitInputCompletion(input, callbacks, terminal, ticket)
		return receipt, nil
	}
	// A provider may be idle while its prior final is still being persisted.
	// Do not overwrite that turn's completion signal or start from Running.
	if err := worker.waitFinalization(ctx); err != nil {
		return receipt, err
	}
	if err := telegramturnhelpers.CheckRootInput(ctx, controller.durableInput, input); err != nil {
		return receipt, err
	}
	current, err := controller.sessions.Load(ctx, input.SessionID)
	if err != nil {
		return receipt, err
	}
	controller.mu.Lock()
	session, usable = controller.usableLocked(current)
	closed = controller.closed
	controller.mu.Unlock()
	if closed || !usable || controller.turnLifecycle != nil && session.Status() != domain.SessionReady {
		return receipt, errors.New("durable input session is not ready for a new turn")
	}
	acceptedSignal := make(chan struct{}, 1)
	resultSignal := make(chan DurableInputProcessReceipt, 1)
	var acceptanceErr error // Published by resultSignal; never read on early ACK.
	admission := turnadmission.NewAdmission()
	turnContext, cancelTurn := context.WithCancel(context.WithoutCancel(ctx))
	stopTurnOnRootCancellation := context.AfterFunc(controller.rootContext, cancelTurn)
	controller.durableWork.Add(1)
	go func() {
		defer controller.durableWork.Done()
		defer stopTurnOnRootCancellation()
		defer cancelTurn()
		acceptedOnce := false
		completion, accepted := worker.runTurnWithAcceptance(turnContext, queuedTurn{
			text: promptText, messageID: input.MessageID, attachments: append([]AttachmentRef(nil), input.Attachments...), admission: admission,
		}, func(callbackCtx context.Context) error {
			if acceptedOnce {
				return errors.New("provider repeated durable acceptance")
			}
			acceptedOnce = true
			acceptanceErr = callbacks.OnAccepted(callbackCtx, DurableInputAcceptance{
				SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
			})
			controller.publishProcessedPromptState(context.WithoutCancel(callbackCtx), input.SessionID, input.MessageID, promptText, "👨‍💻", preprocessingFailed)
			if acceptanceErr != nil {
				return acceptanceErr
			}
			acceptedSignal <- struct{}{}
			return nil
		})
		var commitErr error
		if accepted {
			commitErr = controller.completeInput(callbacks, input, completion)
		}
		admission.FinishRoot(commitErr)
		resultSignal <- DurableInputProcessReceipt{SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence, Completion: completion, Accepted: accepted}
	}()
	select {
	case <-acceptedSignal:
		receipt.Accepted = true
		receipt.Completion = DurableInputPending
		return receipt, nil
	case result := <-resultSignal:
		receipt = result
		if !receipt.Accepted {
			controller.publishProcessedPromptState(context.WithoutCancel(ctx), input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
			return receipt, errors.New("provider did not durably accept input")
		}
		return receipt, acceptanceErr
	case <-ctx.Done():
		cancelTurn()
		controller.publishProcessedPromptState(context.WithoutCancel(ctx), input.SessionID, input.MessageID, promptText, "🙅‍♂", preprocessingFailed)
		return receipt, ctx.Err()
	}
}
func (controller *Controller) stopCurrent(ctx context.Context) coordinator.Decision {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	return controller.stopSession(ctx, active)
}
func (controller *Controller) stopSession(ctx context.Context, sessionID domain.SessionID) coordinator.Decision {
	controller.mu.Lock()
	worker := controller.workers[sessionID]
	controller.mu.Unlock()
	active := sessionID
	if active == "" || worker == nil {
		return controller.status("Нет выполняющегося запроса.")
	}
	turn, activeTurn, claimed := worker.claimConfirmedStop()
	if !activeTurn {
		return controller.status("Нет выполняющегося запроса.")
	}
	if !claimed {
		return controller.status("Остановка запроса уже выполняется для сессии " + string(active) + ".")
	}
	if controller.turnLifecycle != nil {
		stopping, err := controller.turnLifecycle.BeginStop(ctx, active)
		if err != nil {
			worker.releaseFailedStop(turn)
			return controller.status("Не удалось сохранить остановку запроса для сессии " + string(active) + ".")
		}
		controller.replaceLive(stopping)
	}
	if controller.stopper == nil {
		if !worker.cancelClaimedTurn(turn) {
			worker.releaseFailedStop(turn)
			return controller.status("Нет выполняющегося запроса.")
		}
		return controller.status("Остановка запроса отправлена для сессии " + string(active) + ".")
	}
	// Provider interruption waits for a correlated terminal event and may be
	// arbitrarily slow when a CLI is wedged. Never hold the serialized Telegram
	// callback loop on that wait: the durable stopping state above is enough to
	// make repeated taps idempotent, while the provider operation completes in
	// the background.
	controller.durableWork.Add(1)
	go func() {
		defer controller.durableWork.Done()
		if err := controller.stopper.StopCurrent(controller.rootContext, active); err != nil {
			worker.releaseFailedStop(turn)
		}
	}()
	return controller.status("Остановка запроса отправлена для сессии " + string(active) + ".")
}
func (controller *Controller) ensureWorkerLocked(sessionID domain.SessionID) *sessionWorker {
	if existing := controller.workers[sessionID]; existing != nil {
		return existing
	}
	worker := &sessionWorker{
		controller: controller,
		sessionID:  sessionID,
		queue:      make(chan queuedTurn, controller.queueLimit),
	}
	controller.workers[sessionID] = worker
	controller.worker.Add(1)
	go func() {
		defer controller.worker.Done()
		worker.run()
	}()
	return worker
}
func (controller *Controller) usableLocked(session domain.Session) (domain.Session, bool) {
	if session.ID() == "" {
		return domain.Session{}, false
	}
	switch session.Status() {
	case domain.SessionReady, domain.SessionRunning, domain.SessionStopping:
	default:
		return domain.Session{}, false
	}
	live, ok := controller.live[session.ID()]
	return live, ok && live.Equal(session)
}
func (controller *Controller) replaceLive(session domain.Session) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if _, exists := controller.live[session.ID()]; exists {
		controller.live[session.ID()] = session
	}
}
func (worker *sessionWorker) run() {
	for {
		select {
		case <-worker.controller.rootContext.Done():
			return
		default:
		}
		select {
		case <-worker.controller.rootContext.Done():
			return
		case turn := <-worker.queue:
			worker.runTurn(turn)
		}
	}
}
func (worker *sessionWorker) runTurn(turn queuedTurn) {
	_, _ = worker.runTurnWithAcceptance(worker.controller.rootContext, turn, nil)
}
func (worker *sessionWorker) runTurnWithAcceptance(
	ctx context.Context,
	turn queuedTurn,
	onAccepted func(context.Context) error,
) (completion DurableInputCompletion, wasAccepted bool) {
	worker.controller.mu.Lock()
	current := worker.controller.live[worker.sessionID]
	worker.controller.mu.Unlock()
	turnContext, cancelTurn := context.WithCancel(ctx)
	worker.mu.Lock()
	worker.activeTurn++
	activeTurn := worker.activeTurn
	worker.stoppingTurn = 0
	worker.activeCancel = cancelTurn
	terminal := turncompletion.New()
	admission := turn.admission
	if admission == nil {
		admission = turnadmission.NewAdmission()
	}
	worker.completion = terminal
	worker.admission = admission
	worker.completionBinding, _ = current.Binding()
	worker.mu.Unlock()
	var request turnprocessing.Request
	defer func() {
		// Only the durable path previously owned attachment completion. Finish
		// custody once before publishing the same outcome to main and steers.
		if onAccepted != nil && wasAccepted {
			if err := turnprocessing.CompleteAttachments(context.WithoutCancel(ctx), worker.controller.attachments, request); err != nil {
				completion = DurableInputUnknown
				worker.notifyTurnError(turn.messageID+":custody-error", "Не удалось сохранить исход вложений. Очередь приостановлена.")
			}
		}
		if wasAccepted && completion == DurableInputUnknown {
			completion = DurableInputAwaitingRecovery
		}
		admission.Seal()
		terminal.Resolve(string(completion))
		if turn.admission == nil {
			admission.FinishRoot(nil)
		}
	}()
	if worker.controller.turnLifecycle != nil {
		running, err := worker.controller.turnLifecycle.Start(turnContext, worker.sessionID)
		if err != nil {
			cancelTurn()
			worker.clearActiveTurn(activeTurn)
			worker.notifyTurnError(turn.messageID+":lifecycle-start", "Не удалось сохранить начало запроса.")
			return DurableInputFailed, false
		}
		worker.controller.replaceLive(running)
		current = running
	}
	eventIndex := 0
	binding, _ := current.Binding()
	worker.mu.Lock()
	worker.completionBinding = binding
	worker.mu.Unlock()
	request = turnprocessing.Request{
		SessionID: worker.sessionID, ProviderSessionID: binding.SessionID, MessageID: turn.messageID,
		Input: PreparedInput{Text: turn.text, Attachments: append([]AttachmentRef(nil), turn.attachments...)},
	}
	var finishName func(string)
	if worker.controller.sessionNamer != nil {
		finishName = worker.controller.sessionNamer.Begin(worker.controller.rootContext, current, turn.messageID, turn.text)
	}
	execution, err := turnprocessing.Execute(turnContext, worker.controller.submitter, worker.controller.interactions, worker.controller.attachments,
		request, turnprocessing.Callbacks{
			MarkInputAccepted: onAccepted,
			OnEvent: func(event sessionruntime.TurnEvent) error {
				eventIndex++
				worker.emitTurnEvent(turn.messageID, eventIndex, event)
				return nil
			},
		})
	result, accepted, streamedEvents := execution.Result, execution.Accepted, execution.StreamedEvents
	if err != nil {
		worker.controller.closeFlow.Observe(controllertelemetry.WithOperation(context.WithoutCancel(ctx), turn.messageID), controllertelemetry.Event{Stage: controllertelemetry.ProviderFailure, Reason: controllertelemetry.RuntimeFailureReason(sessionruntime.RuntimeFailureClass(err)), Outcome: controllertelemetry.Failed, SessionID: string(worker.sessionID), NodeID: string(current.ComputerID())})
	}
	cancelTurn()
	worker.clearActiveTurn(activeTurn)
	// An accepted transport failure is not a provider terminal. Leave its
	// running/closing recovery target intact for exact supervisor reconciliation.
	unknown := accepted && err != nil && result.TerminalStatus != sessionruntime.StatusCompleted && result.TerminalStatus != sessionruntime.StatusFailed && result.TerminalStatus != sessionruntime.StatusInterrupted
	terminalFailure := accepted && (result.TerminalStatus == sessionruntime.StatusFailed || result.TerminalStatus == sessionruntime.StatusInterrupted)
	terminalStateValid := worker.controller.turnLifecycle == nil && current.Status() == domain.SessionReady
	if err == nil && result.TerminalStatus == sessionruntime.StatusCompleted {
		if !streamedEvents {
			for eventIndex, event := range result.Events {
				worker.emitTurnEvent(turn.messageID, eventIndex+1, event)
			}
		}
		if result.Final != "" {
			if persistErr := worker.controller.persistFinal(worker.controller.rootContext, worker.sessionID, turn.messageID, result.Final, binding); persistErr != nil {
				return DurableInputUnknown, accepted
			}
		}
	}
	if worker.controller.turnLifecycle != nil && !unknown {
		finishContext := context.WithoutCancel(worker.controller.rootContext)
		finished, closeAfter, finishErr := worker.controller.turnLifecycle.Finish(finishContext, worker.sessionID)
		if finishErr != nil {
			worker.notifyTurnError(turn.messageID+":lifecycle-finish", "Не удалось сохранить завершение запроса.")
			return DurableInputFailed, accepted
		}
		worker.controller.replaceLive(finished)
		terminalStateValid = finished.Status() == domain.SessionReady
		if closeAfter {
			finishContext = worker.controller.closeFlow.CompletionContext(finishContext, worker.sessionID)
			if worker.controller.sessionCloser == nil {
				worker.notifyTurnError(turn.messageID+":close-missing", "Сессия ожидает закрытия, но обработчик закрытия не настроен.")
				return DurableInputFailed, accepted
			}
			closed, closeErr := worker.controller.sessionCloser.Close(finishContext, worker.sessionID)
			worker.controller.closeFlow.Outcome(finishContext, worker.sessionID, closed, closeErr, controllertelemetry.ScheduledClose)
			if closeErr != nil {
				worker.notifyTurnError(turn.messageID+":close-error", "Не удалось подтвердить закрытие сессии.")
				return DurableInputFailed, accepted
			}
			if err := worker.controller.applyClosedSession(finishContext, closed.Session); err != nil {
				worker.notifyTurnError(turn.messageID+":close-ui-state", "Не удалось сохранить закрытие сессии.")
				return DurableInputFailed, accepted
			}
			terminalStateValid = closed.Deleted || closed.Session.Status() == domain.SessionArchived
		}
	}
	if finishName != nil {
		finishName(result.ProviderSessionName)
	}
	if err != nil || result.TerminalStatus != sessionruntime.StatusCompleted {
		errorText := "Ошибка CLI: запрос не выполнен."
		if unknown {
			errorText = "Связь с CLI прервалась. Исход запроса пока не подтверждён."
		}
		if result.ErrorCode == sessionruntime.ErrorAuthenticationFailed {
			errorText = "Ошибка авторизации Claude: требуется выполнить вход (/login)."
		}
		if terminalFailure && result.TerminalStatus == sessionruntime.StatusInterrupted {
			errorText = "Запрос остановлен."
		}
		worker.controller.mu.Lock()
		worker.controller.history[worker.sessionID] = append(worker.controller.history[worker.sessionID], errorText)
		worker.controller.mu.Unlock()
		if historyStore, ok := worker.controller.uiState.(CardHistoryStore); ok {
			if err := historyStore.AppendCardHistory(context.WithoutCancel(worker.controller.rootContext), worker.sessionID, errorText); err != nil {
				worker.notifyTurnError(turn.messageID+":terminal-history-error", "Не удалось сохранить исход запроса. Очередь приостановлена.")
				return DurableInputUnknown, accepted
			}
		}
		worker.notifyTurnError(turn.messageID+":error", errorText)
		if unknown {
			return DurableInputUnknown, accepted
		}
		if terminalFailure && terminalStateValid {
			return DurableInputTerminalFailed, accepted
		}
		return DurableInputFailed, accepted
	}
	if worker.controller.finals != nil {
		if err := worker.controller.finals.ProcessFinal(context.WithoutCancel(worker.controller.rootContext), FinalObservation{
			OperationID: turn.messageID + ":final", SessionID: worker.sessionID, MessageID: turn.messageID, Text: result.Final,
		}); err != nil {
			worker.notifyTurnError(turn.messageID+":final-processor-error", "Не удалось обработать итоговые артефакты.")
		}
	}
	if !worker.controller.notify(worker.controller.rootContext, Notification{
		OperationID:    turn.messageID + ":final",
		ConversationID: worker.controller.ownerPrivateChatID,
		SessionID:      worker.sessionID,
		Kind:           NotificationFinal,
		Text:           result.Final,
	}) && worker.controller.durableOutput != nil {
		return DurableInputAwaitingRecovery, accepted
	}
	return DurableInputSucceeded, accepted
}
func (worker *sessionWorker) emitTurnEvent(messageID string, eventIndex int, event sessionruntime.TurnEvent) {
	worker.controller.appendRuntimeHistoryForMessage(worker.controller.rootContext, worker.sessionID, messageID, event)
	kind := NotificationKind("")
	switch event.Kind {
	case sessionruntime.EventCommentary, sessionruntime.EventTool:
		kind = NotificationCommentary
	case sessionruntime.EventQuestion:
		kind = NotificationQuestion
	default:
		return
	}
	operationID := messageID + ":event:" + strconv.Itoa(eventIndex)
	if worker.controller.runtimeEvents != nil {
		if err := worker.controller.runtimeEvents.ObserveRuntimeEvent(context.WithoutCancel(worker.controller.rootContext), RuntimeEventObservation{
			OperationID: operationID, SessionID: worker.sessionID, MessageID: messageID, EventIndex: eventIndex, Event: event,
		}); err != nil {
			worker.notifyTurnError(operationID+":observer-error", "Не удалось обновить Screen для события CLI.")
		}
	}
	text := event.Text
	if event.Kind == sessionruntime.EventTool {
		text = "🛠 Вызов\n||" + strings.TrimSpace(text) + "||"
	}
	worker.controller.notify(worker.controller.rootContext, Notification{
		OperationID:    operationID,
		ConversationID: worker.controller.ownerPrivateChatID,
		SessionID:      worker.sessionID,
		Kind:           kind,
		Text:           text,
	})
}
func (worker *sessionWorker) cancelClaimedTurn(turn uint64) bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeCancel == nil || worker.activeTurn != turn || worker.stoppingTurn != turn {
		return false
	}
	worker.activeCancel()
	return true
}
func (worker *sessionWorker) clearActiveTurn(turn uint64) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeTurn == turn {
		worker.activeCancel = nil
		worker.stoppingTurn = 0
	}
}
func (worker *sessionWorker) hasActiveTurn() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.activeCancel != nil
}
func (worker *sessionWorker) notifyTurnError(operationID, text string) {
	worker.controller.notify(context.WithoutCancel(worker.controller.rootContext), Notification{
		OperationID:    operationID,
		ConversationID: worker.controller.ownerPrivateChatID,
		SessionID:      worker.sessionID,
		Kind:           NotificationError,
		Text:           text,
	})
}
func (worker *sessionWorker) claimConfirmedStop() (turn uint64, active, claimed bool) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeCancel == nil {
		return 0, false, false
	}
	if worker.stoppingTurn == worker.activeTurn {
		return worker.activeTurn, true, false
	}
	worker.stoppingTurn = worker.activeTurn
	return worker.activeTurn, true, true
}
func (worker *sessionWorker) releaseFailedStop(turn uint64) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeTurn == turn && worker.stoppingTurn == turn {
		worker.stoppingTurn = 0
	}
}

// Close prevents new work, cancels every controller worker, and, when a
// Lifecycle is configured, aborts only non-replayed processes created through
// this controller instance. Persisted sessions merely loaded after restart are
// never aborted.
func (controller *Controller) Close(ctx context.Context) error {
	controller.mu.Lock()
	if controller.closed {
		done := controller.closeDone
		controller.mu.Unlock()
		select {
		case <-done:
			controller.mu.Lock()
			err := controller.closeErr
			controller.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	controller.closed = true
	controller.cancelRoot()
	controller.mu.Unlock()
	err := controller.closeWithin(ctx)
	controller.mu.Lock()
	controller.closeErr = err
	close(controller.closeDone)
	controller.mu.Unlock()
	return err
}

type abortResult struct {
	index int
	err   error
}

func (controller *Controller) closeWithin(ctx context.Context) error {
	createsDone := make(chan struct{})
	go func() {
		controller.creates.Wait()
		close(createsDone)
	}()
	select {
	case <-createsDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	controller.mu.Lock()
	created := make([]createdProcess, 0, len(controller.created))
	for _, process := range controller.created {
		created = append(created, process)
	}
	controller.mu.Unlock()
	sort.Slice(created, func(left, right int) bool {
		return created[left].request.SessionID < created[right].request.SessionID
	})
	abortResults := make(chan abortResult, len(created))
	abortPending := 0
	if controller.lifecycle != nil {
		abortPending = len(created)
		for index, process := range created {
			go func() {
				abortResults <- abortResult{
					index: index,
					err:   controller.lifecycle.Abort(ctx, process.request, process.binding),
				}
			}()
		}
	}
	workersDone := make(chan struct{})
	go func() {
		controller.worker.Wait()
		controller.durableWork.Wait()
		close(workersDone)
	}()
	workersPending := true
	abortErrors := make([]error, len(created))
	for abortPending > 0 || workersPending {
		select {
		case result := <-abortResults:
			abortPending--
			abortErrors[result.index] = result.err
		case <-workersDone:
			workersPending = false
			workersDone = nil
		case <-ctx.Done():
			return errors.Join(append(abortErrors, ctx.Err())...)
		}
	}
	if controller.sessionNamer != nil {
		abortErrors = append(abortErrors, controller.sessionNamer.Wait(ctx))
	}
	return errors.Join(abortErrors...)
}
func requestFromSession(session domain.Session) app.StartSessionRequest {
	return app.StartSessionRequest{
		SessionID: session.ID(), ComputerID: session.ComputerID(),
		Provider: session.Provider(), Workdir: session.Workdir(),
	}
}
