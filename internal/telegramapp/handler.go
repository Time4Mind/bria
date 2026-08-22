// Package telegramapp routes private Telegram transport events into
// actor-authorized application use cases.
package telegramapp

import (
	"errors"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerauth"
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramoutbound"
	"github.com/Time4Mind/bria/internal/telegramview"
)

type Messenger = telegramoutbound.Transport

type Handler struct {
	service    *application.Service
	projector  *telegramview.Projector
	tokens     *callbacktoken.Codec
	messenger  Messenger
	controls   SessionControls
	leadership Leadership
	starter    SessionStarter

	paneRefreshState
	voicePendingState
	cardRuntimeState
	providerStopRetryState
	responseCards       responseCardCoordinator
	transcriptTriggers  transcriptTriggerTracker
	fileMu              sync.Mutex
	deliveredFiles      map[string]bool
	createFlows         map[domain.UserID]*createFlow
	flowTTL             time.Duration
	membershipMu        sync.Mutex
	contractFlows       map[domain.UserID]time.Time
	renameFlows         map[domain.UserID]nodeRenameFlow
	providerAliasFlows  map[domain.UserID]providerAliasFlow
	providerAuth        providerauth.Service
	backendSetup        backendsetup.Service
	speechSetup         speechsetup.Service
	clusterUpdater      clusterUpdater
	providerAuthFlows   map[domain.UserID]providerAuthFlow
	providerAuthEpochs  map[domain.UserID]uint64
	nodeSettingsBack    map[domain.UserID]settingsReturn
	statusBack          map[domain.UserID]settingsReturn
	speechMu            sync.Mutex
	speechTargets       map[domain.UserID]domain.NodeID
	knownSpeechNodes    map[domain.NodeID]bool
	speechWatchStarted  time.Time
	sessionStateMu      sync.Mutex
	sessionPages        map[sessionPageKey]cardPageState
	sessionPageTouched  map[sessionPageKey]time.Time
	promptHashes        map[sessionPageKey]string
	promptHashTouched   map[sessionPageKey]time.Time
	sessionStateSweepAt time.Time
	viewMu              sync.Mutex
	visibleCardViews    map[domain.UserID]visibleCardView
	cardTransportMu     sync.Mutex
	cardTransports      map[string]telegrambot.Message
	cardTransportOrder  []string
	activity            *telegramoutbound.Coordinator
	clusterEventMu      sync.Mutex
	clusterEventLogs    map[int64]clusterEventLog
	clusterUpdateMu     sync.Mutex
	clusterUpdateWatch  map[domain.UserID]uint64
	clusterUpdateJobs   map[domain.UserID]string
	clusterAgentWorkdir string
}

func NewHandler(
	service *application.Service,
	projector *telegramview.Projector,
	tokens *callbacktoken.Codec,
	messenger Messenger,
) (*Handler, error) {
	if service == nil || projector == nil || tokens == nil || messenger == nil {
		return nil, errors.New("Telegram application dependencies are required")
	}
	activity := telegramoutbound.New(messenger)
	return &Handler{
		service: service, projector: projector, tokens: tokens, messenger: activity,
		activity:               activity,
		paneRefreshState:       newPaneRefreshState(),
		voicePendingState:      newVoicePendingState(),
		cardRuntimeState:       newCardRuntimeState(),
		providerStopRetryState: newProviderStopRetryState(),
		responseCards:          newResponseCardCoordinator(),
		transcriptTriggers:     newTranscriptTriggerTracker(time.Now()),
		deliveredFiles:         make(map[string]bool),
		createFlows:            make(map[domain.UserID]*createFlow),
		flowTTL:                10 * time.Minute,
		contractFlows:          make(map[domain.UserID]time.Time),
		renameFlows:            make(map[domain.UserID]nodeRenameFlow),
		providerAliasFlows:     make(map[domain.UserID]providerAliasFlow),
		providerAuthFlows:      make(map[domain.UserID]providerAuthFlow),
		providerAuthEpochs:     make(map[domain.UserID]uint64),
		nodeSettingsBack:       make(map[domain.UserID]settingsReturn),
		statusBack:             make(map[domain.UserID]settingsReturn),
		speechTargets:          make(map[domain.UserID]domain.NodeID),
		knownSpeechNodes:       make(map[domain.NodeID]bool),
		speechWatchStarted:     time.Now(),
		sessionPages:           make(map[sessionPageKey]cardPageState),
		sessionPageTouched:     make(map[sessionPageKey]time.Time),
		promptHashes:           make(map[sessionPageKey]string),
		promptHashTouched:      make(map[sessionPageKey]time.Time),
		visibleCardViews:       make(map[domain.UserID]visibleCardView),
		cardTransports:         make(map[string]telegrambot.Message),
		clusterEventLogs:       make(map[int64]clusterEventLog),
		clusterUpdateWatch:     make(map[domain.UserID]uint64),
		clusterUpdateJobs:      make(map[domain.UserID]string),
	}, nil
}

func NewHandlerWithControls(
	service *application.Service,
	projector *telegramview.Projector,
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
	projector *telegramview.Projector,
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
