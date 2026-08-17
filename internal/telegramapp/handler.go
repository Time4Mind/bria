// Package telegramapp routes private Telegram transport events into
// actor-authorized application use cases.
package telegramapp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type Messenger interface {
	AnswerCallbackQuery(context.Context, string, string) error
	SendTyping(context.Context, int64) error
	SendDocument(context.Context, telegrambot.DocumentRequest) (telegrambot.Message, error)
	SendScreen(context.Context, int64, telegramui.Screen) (telegrambot.Message, error)
	EditScreen(context.Context, telegrambot.Message, telegramui.Screen) (telegrambot.Message, error)
	DeleteMessage(context.Context, telegrambot.Message) error
	ClearKeyboard(context.Context, telegrambot.Message) error
}

type Handler struct {
	service    *application.Service
	projector  *application.TelegramProjector
	tokens     *callbacktoken.Codec
	messenger  Messenger
	controls   SessionControls
	leadership Leadership
	starter    SessionStarter

	paneRefreshState
	voicePendingState
	cardRuntimeState
	fileMu             sync.Mutex
	deliveredFiles     map[string]bool
	promptHashes       map[domain.UserID]map[string]string
	createFlows        map[domain.UserID]*createFlow
	flowTTL            time.Duration
	membershipMu       sync.Mutex
	contractFlows      map[domain.UserID]time.Time
	renameFlows        map[domain.UserID]nodeRenameFlow
	providerAliasFlows map[domain.UserID]providerAliasFlow
	providerAuth       providerauth.Service
	backendSetup       backendsetup.Service
	speechSetup        speechsetup.Service
	clusterUpdater     clusterUpdater
	providerAuthFlows  map[domain.UserID]providerAuthFlow
	nodeSettingsBack   map[domain.UserID]settingsReturn
	statusBack         map[domain.UserID]settingsReturn
	speechMu           sync.Mutex
	speechTargets      map[domain.UserID]domain.NodeID
	knownSpeechNodes   map[domain.NodeID]bool
	speechWatchStarted time.Time
	pageMu             sync.Mutex
	sessionPages       map[sessionPageKey]cardPageState
	cardEditMu         sync.Mutex
	activity           *activityMessenger
	clusterEventMu     sync.Mutex
	clusterEventLogs   map[int64]clusterEventLog
}

func NewHandler(
	service *application.Service,
	projector *application.TelegramProjector,
	tokens *callbacktoken.Codec,
	messenger Messenger,
) (*Handler, error) {
	if service == nil || projector == nil || tokens == nil || messenger == nil {
		return nil, errors.New("Telegram application dependencies are required")
	}
	activity := newActivityMessenger(messenger)
	return &Handler{
		service: service, projector: projector, tokens: tokens, messenger: activity,
		activity:           activity,
		paneRefreshState:   newPaneRefreshState(),
		voicePendingState:  newVoicePendingState(),
		cardRuntimeState:   newCardRuntimeState(),
		deliveredFiles:     make(map[string]bool),
		promptHashes:       make(map[domain.UserID]map[string]string),
		createFlows:        make(map[domain.UserID]*createFlow),
		flowTTL:            10 * time.Minute,
		contractFlows:      make(map[domain.UserID]time.Time),
		renameFlows:        make(map[domain.UserID]nodeRenameFlow),
		providerAliasFlows: make(map[domain.UserID]providerAliasFlow),
		providerAuthFlows:  make(map[domain.UserID]providerAuthFlow),
		nodeSettingsBack:   make(map[domain.UserID]settingsReturn),
		statusBack:         make(map[domain.UserID]settingsReturn),
		speechTargets:      make(map[domain.UserID]domain.NodeID),
		knownSpeechNodes:   make(map[domain.NodeID]bool),
		speechWatchStarted: time.Now(),
		sessionPages:       make(map[sessionPageKey]cardPageState),
		clusterEventLogs:   make(map[int64]clusterEventLog),
	}, nil
}

func NewHandlerWithControls(
	service *application.Service,
	projector *application.TelegramProjector,
	tokens *callbacktoken.Codec,
	messenger Messenger,
	controls SessionControls,
) (*Handler, error) {
	handler, err := NewHandler(service, projector, tokens, messenger)
	if err != nil {
		return nil, err
	}
	if controls == nil {
		return nil, errors.New("session controls are required")
	}
	handler.controls = controls
	return handler, nil
}

func NewHandlerWithControlsAndLeadership(
	service *application.Service,
	projector *application.TelegramProjector,
	tokens *callbacktoken.Codec,
	messenger Messenger,
	controls SessionControls,
	leadership Leadership,
) (*Handler, error) {
	if leadership == nil {
		return nil, errors.New("Telegram leadership is required")
	}
	handler, err := NewHandlerWithControls(service, projector, tokens, messenger, controls)
	if err != nil {
		return nil, err
	}
	handler.leadership = leadership
	return handler, nil
}

func (h *Handler) handleCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
) error {
	callback, err := telegramui.DecodeCallback(update.CallbackData)
	if err != nil {
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	if callback.Action == telegramui.ActionNoop {
		return h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, "")
	}
	// Telegram callback payloads do not consistently echo rich_message. Recover
	// the durable carrier metadata when the callback belongs to the current
	// response card, otherwise a rich card is mistaken for a legacy carrier and
	// every rich session selection becomes send+delete instead of an edit.
	update.CallbackOrigin = h.resolveCallbackCarrier(actor, update.CallbackOrigin)
	if leavesSessionCard(callback.Action) {
		h.cancelPaneRefresh(actor.UserID)
	}
	if callback.Action == telegramui.ActionMenu || callback.Action == telegramui.ActionSettings ||
		callback.Action == telegramui.ActionSettingsCategory || callback.Action == telegramui.ActionStatusMode {
		if callback.Action != telegramui.ActionStatusMode || callback.Token != "settings" {
			h.cancelProviderAuthFlow(ctx, actor)
		}
		h.clearMembershipFlows(actor)
	}
	if isCreateAction(callback.Action) {
		// Session creation may browse another node. Clear Telegram's spinner
		// before any language write or node-control round trip.
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		if err := h.ensureLanguage(ctx, actor, update.LanguageCode); err != nil {
			if safeDrop(err) {
				return nil
			}
			return err
		}
		return h.handleCreateCallback(ctx, actor, update, callback)
	}
	if !settingMutation(callback.Action) {
		if err := h.ensureLanguage(ctx, actor, update.LanguageCode); err != nil {
			if safeDrop(err) {
				return h.answerAndDrop(ctx, update.CallbackID,
					h.copy(actor).Text(i18n.ToastUnavailable))
			}
			return err
		}
	}
	if isSessionControlAction(callback.Action) {
		return h.handleSessionControlCallback(ctx, actor, update, callback)
	}
	if isInteractiveAction(callback.Action) {
		return h.handleInteractiveCallback(ctx, actor, update, callback)
	}
	if isMembershipLifecycleAction(callback.Action) {
		return h.handleMembershipLifecycle(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionProviderAuth ||
		callback.Action == telegramui.ActionProviderAuthCancel {
		return h.handleProviderAuthCallback(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionProviderAlias {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
		screen, err := h.beginProviderAlias(actor, callback.Token)
		if safeDrop(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
		return err
	}
	if callback.Action == telegramui.ActionBackendConnect ||
		callback.Action == telegramui.ActionBackendDisconnect {
		return h.handleNodeBackendCallback(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionBackendInstall {
		return h.handleNodeBackendInstallCallback(ctx, actor, update, callback)
	}
	if callback.Action == telegramui.ActionSelectSession {
		// Validate the target before the early acknowledgement below. A session
		// can be archived between rendering its button and receiving the click;
		// that callback is terminal, not a reason to retry the Telegram update.
		ref, resolveErr := h.resolveSession(actor, callback.Action, callback.Token)
		if resolveErr == nil {
			var session domain.Session
			session, resolveErr = h.service.Session(actor, ref)
			if resolveErr == nil && !session.IsLive() {
				resolveErr = domain.ErrInvalidState
			}
		}
		if safeDrop(resolveErr) || errors.Is(resolveErr, domain.ErrInvalidState) {
			return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
		}
		if resolveErr != nil {
			return resolveErr
		}
	}
	// Telegram keeps a callback spinner visible until AnswerCallbackQuery returns.
	// Preference writes may wait for a Raft quorum, so acknowledge those clicks
	// before entering the replicated mutation path. The card itself is still
	// edited only from committed state below.
	answeredEarly := settingMutation(callback.Action) ||
		callback.Action == telegramui.ActionSelectSession ||
		callback.Action == telegramui.ActionSelectNode ||
		isStatusAction(callback.Action) ||
		isArchiveContentAction(callback.Action) ||
		isEnrollmentAction(callback.Action) || isClusterUpdateAction(callback.Action) ||
		callback.Action == telegramui.ActionPagePrevious ||
		callback.Action == telegramui.ActionPageLatest ||
		callback.Action == telegramui.ActionPageNext || isListPageAction(callback.Action)
	if answeredEarly {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
	}
	var screen telegramui.Screen
	var pageRef domain.SessionRef
	switch callback.Action {
	case telegramui.ActionMenu:
		screen, err = h.projector.MainMenu(actor)
	case telegramui.ActionSessions:
		h.clearCreateFlow(actor.UserID)
		if callback.Token == "servers" || isLegacyNodeSessionsScreen(update.CallbackOrigin.Text) {
			h.rememberStatusReturn(actor, true)
			screen, err = h.openStatus(ctx, actor, update, telegramui.StatusChoose, true)
		} else {
			screen, err = h.openSessions(ctx, actor)
		}
	case telegramui.ActionNodesPrevious, telegramui.ActionNodesNext,
		telegramui.ActionSessionsPrevious, telegramui.ActionSessionsNext:
		screen, err = h.openListPage(actor, callback.Action, callback.Token)
	case telegramui.ActionArchive:
		screen, err = h.projector.OpenArchives(actor)
	case telegramui.ActionArchivePrevious, telegramui.ActionArchiveNext:
		screen, err = h.openArchivePage(actor, callback.Action, callback.Token)
	case telegramui.ActionSettings:
		screen, err = h.projector.Settings(actor)
	case telegramui.ActionSettingsCategory:
		screen, err = h.openSettingsCategory(actor, callback.Token)
	case telegramui.ActionOpenSetting:
		screen, err = h.openSetting(actor, callback.Token)
	case telegramui.ActionStatus:
		h.rememberStatusReturn(actor, false)
		screen, err = h.openStatus(ctx, actor, update, telegramui.StatusChoose, true)
	case telegramui.ActionStatusRefresh:
		screen, err = h.openStatus(ctx, actor, update, statusMode(callback.Token), true)
	case telegramui.ActionStatusMode:
		screen, err = h.projectStatus(actor, statusMode(callback.Token))
	case telegramui.ActionStatusLeaderNode:
		screen, err = h.confirmStatusLeader(actor, callback.Token)
	case telegramui.ActionSetLeaderNode:
		screen, err = h.confirmClusterLeader(actor, callback.Token)
	case telegramui.ActionStatusSettingsNode:
		if isLegacyNodeSessionsScreen(update.CallbackOrigin.Text) {
			screen, err = h.openLegacyNodeSettingsFromSessions(actor, callback.Token)
		} else {
			screen, err = h.openStatusNodeSettings(actor, callback.Token)
		}
	case telegramui.ActionNodeSettings:
		screen, err = h.openNodeSettingsFromSessions(actor, callback.Token)
	case telegramui.ActionNodeBackends:
		screen, err = h.openNodeBackends(actor, callback.Token)
	case telegramui.ActionNodeBackend:
		screen, err = h.openNodeBackend(actor, callback.Token)
	case telegramui.ActionNodeSpeechSetup:
		screen, err = h.setupNodeSpeech(ctx, actor, callback.Token)
	case telegramui.ActionNodeSpeechBack:
		screen, err = h.backToNodeSpeechSettings(actor, callback.Token)
	case telegramui.ActionConfirmLeader:
		screen, err = h.applyStatusLeader(ctx, actor, callback.Token)
	case telegramui.ActionSetLeaderMode:
		screen, err = h.updateLeaderMode(ctx, actor, callback.Token)
	case telegramui.ActionClusterAdd:
		screen = telegramui.RenderEnrollmentMethods(h.copy(actor))
	case telegramui.ActionClusterInvite:
		screen, err = h.createEnrollmentInvitation(ctx, actor)
	case telegramui.ActionClusterContract:
		screen = h.beginNodeContract(actor)
	case telegramui.ActionClusterUpdate, telegramui.ActionClusterUpdateYes,
		telegramui.ActionClusterUpdateRefresh:
		screen, err = h.handleClusterUpdate(ctx, actor, callback.Action)
	case telegramui.ActionEnrollmentOpen:
		screen, err = h.openEnrollment(actor, telegramui.ActionEnrollmentOpen, callback.Token)
	case telegramui.ActionEnrollmentApprove, telegramui.ActionEnrollmentReject:
		screen, err = h.decideEnrollment(ctx, actor, callback.Action, callback.Token)
	case telegramui.ActionSetLanguage, telegramui.ActionSetSessionView,
		telegramui.ActionSetResumeSelection, telegramui.ActionSetIdleArchive,
		telegramui.ActionSetRetention, telegramui.ActionSetExpiry, telegramui.ActionSetToolCalls,
		telegramui.ActionSetToolResults, telegramui.ActionSetToolOutputLines, telegramui.ActionSetThinking,
		telegramui.ActionSetResponseCards, telegramui.ActionSetNotifyFinished,
		telegramui.ActionSetTerminalSnapshots,
		telegramui.ActionSetNotifyError, telegramui.ActionSetNotifyAction,
		telegramui.ActionSetBgDismiss, telegramui.ActionSetNodeSort,
		telegramui.ActionSetQuotaPoll, telegramui.ActionSetOfflineQueue:
		screen, err = h.updateSettings(ctx, actor, callback, update.LanguageCode)
	case telegramui.ActionSetVoiceBackend:
		screen, err = h.updateVoiceSetting(ctx, actor, callback, update.LanguageCode)
	case telegramui.ActionConfirmVoiceEnable:
		screen, err = h.confirmVoiceEnable(ctx, actor, update.LanguageCode)
	case telegramui.ActionCancelVoiceEnable:
		h.takeSpeechTarget(actor.UserID)
		screen, err = h.projector.Setting(actor, telegramui.SettingVoiceBackend)
	case telegramui.ActionSelectNode:
		screen, err = h.selectNode(ctx, actor, callback.Token)
	case telegramui.ActionSelectArchiveNode:
		screen, err = h.selectArchiveNode(ctx, actor, callback.Token)
	case telegramui.ActionSelectSession:
		screen, err = h.selectSession(ctx, actor, callback.Action, callback.Token)
	case telegramui.ActionPagePrevious, telegramui.ActionPageLatest, telegramui.ActionPageNext:
		screen, pageRef, err = h.openSessionPage(
			ctx, actor, callback.Action, callback.Token,
		)
	case telegramui.ActionSelectArchive:
		screen, err = h.openArchive(ctx, actor, callback.Token)
	case telegramui.ActionArchiveBack:
		screen, err = h.backToArchive(actor, callback.Token)
	case telegramui.ActionArchiveHistory, telegramui.ActionHistoryPrevious,
		telegramui.ActionHistoryNext:
		screen, err = h.openArchiveHistory(ctx, actor, callback.Action, callback.Token)
	default:
		return nil
	}
	if callback.Action == telegramui.ActionSelectSession &&
		errors.Is(err, domain.ErrInvalidState) {
		// A card can become stale between rendering and the click (for example,
		// when reboot recovery archives a failed resume). Retrying cannot make
		// that historical selection valid and would block every later update.
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	if safeDrop(err) {
		if answeredEarly {
			return nil
		}
		return h.answerAndDrop(ctx, update.CallbackID, h.copy(actor).Text(i18n.ToastUnavailable))
	}
	if err != nil {
		return err
	}
	if !answeredEarly {
		if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
			return err
		}
	}
	var edited telegrambot.Message
	targetRich := screen.RichMarkdown || screen.Pane != nil
	replaceCarrier := !update.CallbackOrigin.Rich && targetRich
	if replaceCarrier {
		// A legacy card cannot acquire Rich Markdown by being edited through the
		// legacy endpoint: Telegram accepts the text but shows details, fences,
		// and tables literally. Replace it on the first transition regardless of
		// which action exposed the rich screen (including Sessions and paging).
	}
	pageEdit := pageRef.Validate() == nil
	serializeCardEdit := pageEdit || leavesSessionCard(callback.Action)
	if pageEdit {
		// Stop the live worker before serializing the page edit. If it already
		// owns the edit lock, it finishes first; the user's explicit page always
		// wins and the replacement worker starts from that pinned page.
		h.cancelPaneRefresh(actor.UserID)
		h.rememberResolvedCardPage(actor.UserID, pageRef, screen)
	}
	if serializeCardEdit {
		// An explicit navigation must be the final writer. The live worker uses
		// the same lock, so a pane edit that was already in flight finishes first
		// and cannot repaint the session after the requested screen is visible.
		h.cardEditMu.Lock()
	}
	if replaceCarrier {
		// Telegram cannot safely promote a legacy carrier to Rich in place.
		// A Rich carrier can render a plain projection without being replaced;
		// retaining it keeps the keyboard that the user just tapped valid.
		edited, err = h.messenger.SendScreen(ctx, update.ChatID, screen)
		if err == nil {
			_ = h.messenger.DeleteMessage(ctx, update.CallbackOrigin)
		}
	} else {
		edited, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
	}
	if err == nil {
		// Persist every carrier screen, including non-session navigation. An empty
		// session checkpoint is intentional state: background workers must not
		// infer that the selected session is still visible.
		h.rememberResponseCard(ctx, actor, edited, screen)
		if leavesSessionCard(callback.Action) {
			// A background reconciliation can start a replacement worker while a
			// slow navigation screen is being projected. Invalidate that generation
			// after the non-session checkpoint is durable and before releasing the
			// shared edit lock.
			h.cancelPaneRefresh(actor.UserID)
		}
	}
	if serializeCardEdit {
		h.cardEditMu.Unlock()
	}
	if err == nil && (callback.Action == telegramui.ActionStatus ||
		callback.Action == telegramui.ActionStatusRefresh) {
		h.scheduleStatusRefresh(ctx, actor, edited, statusMode(callback.Token))
	}
	if err == nil && pageRef.Validate() == nil {
		h.rememberResolvedCardPage(actor.UserID, pageRef, screen)
	} else if err == nil {
		h.rememberCardPage(actor.UserID, screen)
	}
	if err == nil && callback.Action == telegramui.ActionSelectSession && h.controls != nil {
		if ref, resolveErr := h.resolveSession(actor, callback.Action, callback.Token); resolveErr == nil {
			h.rememberResolvedCardPage(actor.UserID, ref, screen)
			h.schedulePaneRefresh(ctx, actor, ref, edited)
		}
	}
	if err == nil && pageEdit && h.controls != nil && h.sessionNeedsPaneRefresh(actor, pageRef) {
		h.schedulePaneRefresh(ctx, actor, pageRef, edited)
	}
	return err
}

func (h *Handler) selectArchiveNode(
	ctx context.Context,
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	candidates, err := h.service.CallbackNodeCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeID, err := h.tokens.ResolveNode(
		actor.UserID, telegramui.ActionSelectArchiveNode, token, candidates,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.SelectNode(ctx, actor, nodeID); err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.NodeArchives(actor, nodeID)
}
