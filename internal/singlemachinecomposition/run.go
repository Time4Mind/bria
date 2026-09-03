package singlemachinecomposition

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bria/internal/app"
	"bria/internal/authcomposition"
	"bria/internal/callbacktoken"
	"bria/internal/claudestore"
	"bria/internal/config"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/durablecomposition"
	"bria/internal/durableflow"
	"bria/internal/interactioncomposition"
	"bria/internal/messagejournal"
	"bria/internal/recoverycomposition"
	"bria/internal/recoveryruntime"
	"bria/internal/runtimefactory"
	"bria/internal/safelog"
	"bria/internal/sessionexpiry"
	"bria/internal/sessionid"
	"bria/internal/sessionruntime"
	"bria/internal/sessionsupervisor"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/storage"
	"bria/internal/supervisioncomposition"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegramnotify"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramrecoverycomposition"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/turnruntimecomposition"
	"bria/internal/workdir"
)

const controllerCloseTimeout = 5 * time.Second

const (
	sessionExpiryInterval   = time.Minute
	safeLogCleanupInterval  = time.Minute
	telegramCallbackTTL     = 15 * time.Minute
	durableLeaseDuration    = time.Minute
	telegramStatusInterval  = time.Second
	telegramStatusBatch     = 100
	outboundReceiptInterval = 200 * time.Millisecond
)

type InstanceLock interface {
	Close() error
}

type ProviderRuntime interface {
	app.SessionStarter
	sessionruntime.Submitter
}

type liveConfigStarter struct {
	base  app.SessionStarter
	store config.Store
}

func (starter liveConfigStarter) Start(ctx context.Context, request app.StartSessionRequest) (domain.ProviderBinding, error) {
	if starter.base == nil || starter.store == nil {
		return domain.ProviderBinding{}, errors.New("live provider starter is not configured")
	}
	if request.Mode == app.SessionStartNew {
		snapshot, err := starter.store.Current(ctx)
		if err != nil {
			return domain.ProviderBinding{}, fmt.Errorf("read live provider configuration: %w", err)
		}
		if !snapshot.Config.ProviderEnabled(request.Provider) {
			return domain.ProviderBinding{}, errors.New("provider is disabled or unconfigured")
		}
	}
	return starter.base.Start(ctx, request)
}

func (starter liveConfigStarter) Abort(ctx context.Context, request app.StartSessionRequest, binding domain.ProviderBinding) error {
	if starter.base == nil {
		return errors.New("live provider starter is not configured")
	}
	return starter.base.Abort(ctx, request, binding)
}

type recoveredProviderRuntime struct {
	*sessionruntime.Starter
	reader sessionruntime.AcceptedTurnReader
}

func (runtime recoveredProviderRuntime) ReadAcceptedTurns(ctx context.Context, request sessionruntime.AcceptedTurnReadRequest) (sessionruntime.AcceptedTurnReconciliation, error) {
	if runtime.reader == nil {
		return sessionruntime.AcceptedTurnReconciliation{}, recoveryruntime.ErrUnavailable
	}
	return runtime.reader.ReadAcceptedTurns(ctx, request)
}

// Dependencies supply the local process and Telegram boundaries to Run.
type Dependencies struct {
	AcquireLock    func(string) (InstanceLock, error)
	Executable     func() (string, error)
	Environment    func() []string
	TelegramHTTP   func() telegram.HTTPClient
	ComposeRuntime func(config.Config, []string, string, sessionruntime.Options) (ProviderRuntime, error)
}

type confirmedReadiness struct{}

func (confirmedReadiness) Ready(context.Context, coordinator.Checkpoint) error { return nil }

type unavailableProviderRuntime struct{}

type replyRouteRecorder struct {
	store *storage.TelegramReplyRouteStore
}

type expirySessionCloser struct {
	closer *app.SessionCloser
}

func (adapter expirySessionCloser) Close(ctx context.Context, id domain.SessionID) error {
	if adapter.closer == nil {
		return errors.New("session closer is required")
	}
	_, err := adapter.closer.Close(ctx, id)
	return err
}

func (recorder replyRouteRecorder) RecordOutboundReceipt(ctx context.Context, receipt telegramnotify.OutboundReceipt) error {
	if recorder.store == nil {
		return errors.New("Telegram reply route store is required")
	}
	return recorder.store.RecordOutboundReceipt(ctx, storage.TelegramOutboundReceipt{
		MessageID: receipt.MessageID,
		SessionID: receipt.SessionID,
	})
}

func (unavailableProviderRuntime) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("no provider is enabled")
}

func (unavailableProviderRuntime) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return errors.New("no provider is enabled")
}

func (unavailableProviderRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("no provider is enabled")
}

func runTelegramController(
	ctx context.Context,
	configPath string,
	dependencies Dependencies,
) (returnErr error) {
	configuration, err := config.LoadFile(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := validateRunnableRole(configuration); err != nil {
		return err
	}
	lock, err := dependencies.AcquireLock(configuration.StatePath)
	if err != nil {
		return fmt.Errorf("acquire exclusive instance lock: %w", err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			returnErr = fmt.Errorf("release exclusive instance lock: %w", closeErr)
		}
	}()

	token, err := configuration.ReadToken()
	if err != nil {
		return fmt.Errorf("read Telegram token: %w", err)
	}
	defer clear(token)
	callbackKey, err := configuration.ReadCallbackKey()
	if err != nil {
		return fmt.Errorf("read callback key: %w", err)
	}
	defer clear(callbackKey)

	client, err := telegram.NewClient(
		string(token),
		dependencies.TelegramHTTP(),
		telegram.Options{},
	)
	if err != nil {
		return fmt.Errorf("create Telegram client: %w", err)
	}
	readiness, err := telegrambridge.NewPersistentReadiness(client, configuration.BotUsername)
	if err != nil {
		return fmt.Errorf("create Telegram readiness check: %w", err)
	}
	// Prove the exact Telegram identity before opening durable state or
	// starting/recovering any provider process. The coordinator receives the
	// already-confirmed gate so this single preflight is not repeated.
	if err := readiness.Ready(ctx, coordinator.Checkpoint{}); err != nil {
		return fmt.Errorf("verify coordinator readiness: %w", err)
	}
	computerID, err := configuredComputerID(configuration)
	if err != nil {
		return fmt.Errorf("resolve local computer identity: %w", err)
	}

	state, err := storage.OpenSessionStore(configuration.StatePath)
	if err != nil {
		return fmt.Errorf("open durable state: %w", err)
	}
	preferences, err := settings.OpenFileStore(configuration.StatePath + ".settings.json")
	if err != nil {
		return fmt.Errorf("open Bria settings: %w", err)
	}
	providerPreferences, err := config.OpenFileStore(configPath)
	if err != nil {
		return fmt.Errorf("open provider settings: %w", err)
	}
	effectiveSettings, err := loadEffectiveSettings(ctx, preferences)
	if err != nil {
		return fmt.Errorf("load Bria settings: %w", err)
	}
	sessionLifetime, err := mapSessionLifetime(effectiveSettings.SessionLifetime)
	if err != nil {
		return fmt.Errorf("compose Bria settings: %w", err)
	}
	replyRoutes, err := storage.OpenTelegramReplyRouteStore(
		configuration.StatePath+".telegram-reply-routes.json",
		configuration.OwnerUserID,
		configuration.PrivateChatID,
	)
	if err != nil {
		return fmt.Errorf("open Telegram reply routes: %w", err)
	}
	partReceipts, err := telegramnotify.OpenFilePartReceiptStore(configuration.StatePath + ".telegram-notification-parts.json")
	if err != nil {
		return fmt.Errorf("open Telegram notification part receipts: %w", err)
	}
	notifier, err := telegramnotify.NewWithOptions(client, telegramnotify.Options{
		ReceiptRecorder: replyRouteRecorder{store: replyRoutes}, PartReceipts: partReceipts,
	})
	if err != nil {
		return fmt.Errorf("create Telegram notifier: %w", err)
	}
	journalLimits := messagejournal.DefaultLimits()
	journalLimits.MaxPendingInputsPerSession = effectiveSettings.QueueLimit
	journal, err := messagejournal.Open(configuration.StatePath+".message-journal.json", journalLimits)
	if err != nil {
		return fmt.Errorf("open durable message journal: %w", err)
	}
	clock := func() time.Time { return time.Now().UTC() }
	flow, err := durableflow.New(journal, nil, durablecomposition.TelegramOutputSender{
		OwnerPrivateChatID: configuration.PrivateChatID, Deliverer: notifier,
	}, durableflow.Options{Owner: string(computerID), LeaseDuration: durableLeaseDuration, Now: clock})
	if err != nil {
		return fmt.Errorf("create durable message flow: %w", err)
	}
	safeLogger, err := safelog.Open(safelog.Options{Directory: configuration.StatePath + ".logs"})
	if err != nil {
		return fmt.Errorf("open safe operational log: %w", err)
	}
	inputWake := make(chan domain.SessionID, 256)
	outputWake := make(chan domain.SessionID, 256)
	inputCustody := durablecomposition.InputCustody{Flow: flow, Wake: inputWake}
	outputCustody := durablecomposition.OutputCustody{
		Flow: flow, Wake: outputWake, OwnerPrivateChatID: configuration.PrivateChatID,
	}
	checkpoints := state.CoordinatorCheckpoints()
	executable, err := dependencies.Executable()
	if err != nil {
		return errors.New("resolve Bria executable")
	}
	starter, err := dependencies.ComposeRuntime(
		configuration,
		dependencies.Environment(),
		executable,
		sessionruntime.Options{},
	)
	if err != nil {
		return fmt.Errorf("compose provider runtime: %w", err)
	}
	turnRuntime, err := turnruntimecomposition.Open(turnruntimecomposition.Options{
		Configuration: configuration, Telegram: client, Settings: preferences, Sessions: state, Runtime: starter, Logger: safeLogger,
	})
	if err != nil {
		return err
	}
	controllerSubmitter := turnRuntime.Submitter
	inputPreparer, attachments := turnRuntime.InputPreparer, turnRuntime.Attachments
	runtimeEvents, finals := turnRuntime.RuntimeEvents, turnRuntime.Finals
	var recovery app.SessionRecoveryResult
	var processSupervision contextRunner = idleRunner{}
	waiter, canWait := starter.(sessionruntime.ProcessSupervisor)
	reader, canReconcile := starter.(sessionruntime.AcceptedTurnReader)
	if canWait && canReconcile {
		providerHistory, err := recoverycomposition.NewReconciler(reader, state)
		if err != nil {
			return fmt.Errorf("compose provider recovery reader: %w", err)
		}
		durableRecovery := durablecomposition.AcceptedTurnReconciler{Flow: flow, Histories: map[domain.Provider]sessionsupervisor.AcceptedTurnReconciler{
			domain.ProviderCodex: providerHistory, domain.ProviderClaude: providerHistory,
		}}
		reportRecovery := func(reportErr error) {
			_ = safeLogger.Write(safelog.Event{Class: safelog.Critical, Type: "session.recovery_failed", ErrorCategory: "session_recovery", Error: reportErr.Error(), Fields: map[string]string{"component": "sessionsupervisor", "operation": "recover"}})
		}
		supervision, err := supervisioncomposition.New(supervisioncomposition.Options{
			LocalComputerID: computerID, Store: state, Waiter: waiter, Restarter: starter,
			AcceptedTurns: durableRecovery, MaxRestartAttempts: 3, SweepInterval: time.Second,
			Now: clock, WaitBeforeRetry: waitRecoveryRetry, Report: reportRecovery,
		})
		if err != nil {
			return fmt.Errorf("compose session supervision: %w", err)
		}
		recovery, err = supervision.RecoverStartup(ctx)
		processSupervision = supervision
	} else {
		if err = supervisioncomposition.RequireSafeFallback(ctx, state); err == nil {
			recovery, err = app.RecoverPersistedSessionsForComputer(ctx, computerID, state, starter)
		}
	}
	if err != nil {
		return fmt.Errorf("recover persisted sessions: %w", err)
	}
	creator, err := app.NewSessionCreator(
		computerID,
		workdir.ExistingDirectory{},
		sessionid.New(),
		state,
		liveConfigStarter{base: starter, store: providerPreferences},
		app.WithSessionLifetime(sessionLifetime),
	)
	if err != nil {
		return fmt.Errorf("create session service: %w", err)
	}
	archivedResumer, err := app.NewArchivedSessionResumer(state, starter, sessionLifetime, clock)
	if err != nil {
		return fmt.Errorf("create archived session resumer: %w", err)
	}
	sessionCloser, err := app.NewSessionCloser(state, starter, clock)
	if err != nil {
		return fmt.Errorf("create session closer: %w", err)
	}
	turnLifecycle, err := app.NewSessionTurnLifecycle(state, clock)
	if err != nil {
		return fmt.Errorf("create session turn lifecycle: %w", err)
	}
	expiry, err := sessionexpiry.New(state, expirySessionCloser{closer: sessionCloser}, clock)
	if err != nil {
		return fmt.Errorf("create session expiry scheduler: %w", err)
	}
	expiryReporter := func(reportErr error) {
		_ = safeLogger.Write(safelog.Event{
			Class: safelog.Critical, Type: "session.expiry_failed", ErrorCategory: "session_expiry",
			Error:  reportErr.Error(),
			Fields: map[string]string{"component": "sessionexpiry", "operation": "sweep"},
		})
	}
	var turnStopper sessionruntime.TurnStopper
	if configuredStopper, ok := starter.(sessionruntime.TurnStopper); ok {
		turnStopper = configuredStopper
	}
	interactions, err := interactioncomposition.Open(interactioncomposition.Options{
		StorePath:      configuration.StatePath + ".provider-interactions.json",
		ConversationID: configuration.PrivateChatID, OwnerActorID: configuration.OwnerUserID, Telegram: client,
	})
	if err != nil {
		return fmt.Errorf("compose provider interactions: %w", err)
	}
	var authorization telegramcontroller.AuthorizationFlow
	codexProvider, claudeProvider := configuration.Providers[string(domain.ProviderCodex)], configuration.Providers[string(domain.ProviderClaude)]
	if codexProvider.Command != nil || claudeProvider.Command != nil {
		var codexExecutable, claudeExecutable, claudeCredentialPath string
		if codexProvider.Command != nil {
			codexExecutable = codexProvider.Command.Exec
		}
		if claudeProvider.Command != nil {
			claudeExecutable = claudeProvider.Command.Exec
			claudeCredentialPath = configuration.StatePath + ".claude-api-key.json"
		}
		authorization, err = authcomposition.Open(authcomposition.Options{
			OwnerID: configuration.OwnerUserID, LocalComputerID: computerID,
			StorePath: configuration.StatePath + ".authorization.json", Telegram: client,
			CodexExecutable: codexExecutable, ClaudeExecutable: claudeExecutable, ClaudeCredentialPath: claudeCredentialPath,
		})
		if err != nil {
			return fmt.Errorf("compose provider authorization: %w", err)
		}
	}
	handler, err := telegramcontroller.New(
		configuration.OwnerUserID,
		configuration.PrivateChatID,
		computerID,
		creator,
		state,
		controllerSubmitter,
		notifier,
		telegramcontroller.Options{
			QueueLimit: effectiveSettings.QueueLimit, Lifecycle: starter, UIState: state,
			Settings: settings.NewTelegramPreferences(preferences), Providers: settingscomposition.ProviderPreferences{Store: providerPreferences}, ReplyRoutes: replyRoutes,
			Stopper: turnStopper, ArchivedResumer: archivedResumer, SessionCloser: sessionCloser,
			TurnLifecycle: turnLifecycle, DurableInput: inputCustody, DurableOutput: outputCustody,
			InputPreparer: inputPreparer, Attachments: attachments, RuntimeEvents: runtimeEvents, Finals: finals,
			Interactions: interactions.Flow(), InteractionText: interactions.Flow(), Authorization: authorization,
			Recovered: recovery.Sessions,
		},
	)
	if err != nil {
		return fmt.Errorf("create Telegram controller: %w", err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), controllerCloseTimeout)
		defer cancel()
		if closeErr := handler.Close(closeContext); closeErr != nil {
			returnErr = fmt.Errorf("close Telegram controller: %w", closeErr)
		}
	}()

	source, err := telegrambridge.NewSource(client)
	if err != nil {
		return fmt.Errorf("create Telegram update source: %w", err)
	}
	transportSender, err := telegrambridge.NewSender(client)
	if err != nil {
		return fmt.Errorf("create Telegram sender: %w", err)
	}
	callbackCodec, err := callbacktoken.New(callbackKey, nil, clock)
	if err != nil {
		return fmt.Errorf("create callback token codec: %w", err)
	}
	presenter, err := telegrambridge.NewPresenter(callbackCodec, clock, telegramCallbackTTL)
	if err != nil {
		return fmt.Errorf("create Telegram presenter: %w", err)
	}
	callbackRegistry, err := telegrampipeline.OpenFileCallbackRegistry(configuration.StatePath+".callback-registry.json", clock)
	if err != nil {
		return fmt.Errorf("open callback registry: %w", err)
	}
	callbackOperations, err := telegramflow.OpenFileCallbackOperationStore(configuration.StatePath + ".callback-operations.json")
	if err != nil {
		return fmt.Errorf("open callback operation store: %w", err)
	}
	controllerAdapter := telegramruntimecomposition.ControllerFlowAdapter{Controller: handler}
	callbackRouter, err := interactioncomposition.NewCallbackRouter(controllerAdapter, interactions.Flow())
	if err != nil {
		return fmt.Errorf("compose Telegram callback router: %w", err)
	}
	recoveryExecutor, err := telegramrecoverycomposition.New(callbackRouter, callbackOperations, telegramruntimecomposition.CurrentProjector{Controller: handler})
	if err != nil {
		return fmt.Errorf("compose Telegram recovery executor: %w", err)
	}
	callbackExecutor := turnRuntime.WrapCallback(telegramflow.CallbackExecutor(recoveryExecutor))
	flowHandler, flowSender, err := telegramflow.New(telegramflow.Config{
		OwnerUserID: configuration.OwnerUserID, OwnerPrivateChatID: configuration.PrivateChatID,
		Presenter: presenter, CallbackRegistry: callbackRegistry,
		UIState: telegramruntimecomposition.SessionTelegramUIStore{State: state}, MessageUI: controllerAdapter,
		Callbacks: callbackExecutor, Operations: callbackOperations, Sender: transportSender,
	})
	if err != nil {
		return fmt.Errorf("create signed Telegram flow: %w", err)
	}
	if err := turnRuntime.BindPublisher(presenter, flowSender); err != nil {
		return fmt.Errorf("bind artifact retry publisher: %w", err)
	}
	if err := interactions.Bind(flowSender, presenter); err != nil {
		return fmt.Errorf("bind provider interaction delivery: %w", err)
	}
	if err := recoveryExecutor.Bind(flowSender); err != nil {
		return fmt.Errorf("bind Telegram recovery executor: %w", err)
	}
	durableReporter := func(component, eventType string) func(error) {
		return func(reportErr error) {
			_ = safeLogger.Write(safelog.Event{
				Class: safelog.Critical, Type: eventType, ErrorCategory: "durable_flow",
				Error: reportErr.Error(), Fields: map[string]string{"component": component, "operation": "dispatch"},
			})
		}
	}
	inputDispatcher := durablecomposition.InputDispatcher{
		Flow: flow, Processor: durablecomposition.NewControllerInputProcessor(handler), Sessions: state,
		Wake: inputWake, Report: durableReporter("durable_input", "durable.input_failed"),
	}
	outputDispatcher := durablecomposition.OutputDispatcher{
		Flow: flow, Sessions: state, Wake: outputWake,
		Report: durableReporter("durable_output", "durable.output_failed"),
	}
	loop, err := coordinator.NewLoop(source, checkpoints, flowHandler, flowSender, confirmedReadiness{})
	if err != nil {
		return fmt.Errorf("create coordinator: %w", err)
	}
	statusDelivery := telegramruntimecomposition.StatusDeliveryRunner{
		Delivery: flowSender, Checkpoints: checkpoints, Confirmer: loop,
		Interval: telegramStatusInterval, Limit: telegramStatusBatch,
		Report: durableReporter("telegram_status", "telegram.status_delivery_failed"),
	}
	receiptReconciler := telegramruntimecomposition.OutboundReceiptReconciler{
		Checkpoints: checkpoints, Confirmer: loop, Interval: outboundReceiptInterval,
		Report: durableReporter("coordinator_receipt", "telegram.status_receipt_failed"),
	}
	runErr := runControllerWithMaintenance(ctx, loop, expiry, safeLogger, inputDispatcher, outputDispatcher, statusDelivery, receiptReconciler, processSupervision, expiryReporter)
	// A shutdown can race immediately after Telegram returned its receipt but
	// before the periodic projection into the outer checkpoint. Reconcile once
	// more from durable inner state using a fresh bounded local context.
	reconcileContext, cancelReconcile := context.WithTimeout(context.Background(), controllerCloseTimeout)
	reconcileErr := telegramruntimecomposition.ReconcileEnqueuedOutbound(reconcileContext, checkpoints, loop)
	cancelReconcile()
	if reconcileErr != nil && !errors.Is(reconcileErr, coordinator.ErrDeliveryUnknown) {
		runErr = errors.Join(runErr, fmt.Errorf("reconcile durable Telegram receipt: %w", reconcileErr))
	}
	if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return runErr
	}
	logErr := safeLogger.Write(safelog.Event{
		Class: safelog.Critical, Type: "controller.stopped", ErrorCategory: "runtime_failure",
		Error:  runErr.Error(),
		Fields: map[string]string{"component": "controller", "operation": "run"},
	})
	if logErr != nil {
		return errors.Join(runErr, fmt.Errorf("persist safe controller failure: %w", logErr))
	}
	return runErr
}

// Run starts the local combined runtime from one validated configuration path.
func Run(ctx context.Context, configPath string, dependencies Dependencies) error {
	if dependencies.AcquireLock == nil || dependencies.Executable == nil || dependencies.Environment == nil || dependencies.TelegramHTTP == nil || dependencies.ComposeRuntime == nil {
		return errors.New("runtime composition dependencies are required")
	}
	return runTelegramController(ctx, configPath, dependencies)
}

// ComposeProviderRuntime derives runtime and accepted-turn recovery from one
// immutable command snapshot. Archive discovery is intentionally not inferred.
func ComposeProviderRuntime(configuration config.Config, environment []string, executable string, options sessionruntime.Options) (ProviderRuntime, error) {
	if !configuration.ProviderEnabled(domain.ProviderCodex) && !configuration.ProviderEnabled(domain.ProviderClaude) {
		return unavailableProviderRuntime{}, nil
	}
	commands, err := runtimefactory.NewConfiguredCommandSet(configuration, environment, executable)
	if err != nil {
		return nil, err
	}
	starter, err := commands.NewStarter(options)
	if err != nil {
		return nil, err
	}
	reader, err := composeAcceptedTurnReader(configuration, commands)
	if err != nil {
		return nil, err
	}
	if reader == nil {
		return starter, nil
	}
	return recoveredProviderRuntime{Starter: starter, reader: reader}, nil
}

func composeAcceptedTurnReader(configuration config.Config, commands *runtimefactory.CommandSet) (sessionruntime.AcceptedTurnReader, error) {
	discovery, enabled := configuration.DiscoveryRuntime()
	if !enabled {
		return nil, nil
	}
	var codex, claude sessionruntime.AcceptedTurnReader
	if configuration.ProviderEnabled(domain.ProviderCodex) {
		specification, ok := commands.CommandSpec(domain.ProviderCodex)
		if !ok {
			return nil, errors.New("configured Codex recovery command is unavailable")
		}
		reader, err := recoveryruntime.New(specification, recoveryruntime.Options{})
		if err != nil {
			return nil, fmt.Errorf("compose Codex accepted-turn reader: %w", err)
		}
		codex = reader
	}
	if configuration.ProviderEnabled(domain.ProviderClaude) {
		transcripts, err := claudestore.NewTranscriptStore(discovery.ClaudeRoot, claudestore.TranscriptStoreOptions{})
		if err != nil {
			return nil, fmt.Errorf("open Claude transcript root: %w", err)
		}
		reader, err := recoveryruntime.NewClaude(transcripts)
		if err != nil {
			return nil, fmt.Errorf("compose Claude accepted-turn reader: %w", err)
		}
		claude = reader
	}
	if codex != nil && claude != nil {
		readers, err := recoveryruntime.NewProviderReaders(codex, claude)
		if err != nil {
			return nil, fmt.Errorf("compose provider accepted-turn readers: %w", err)
		}
		return readers, nil
	}
	if codex != nil {
		return codex, nil
	}
	return claude, nil
}

func validateRunnableRole(configuration config.Config) error {
	if configuration.IsLegacy() {
		return nil
	}
	switch configuration.EffectiveRole() {
	case config.RoleExecutor:
		return errors.New("executor runtime is not connected in this build")
	case config.RoleCoordinator:
		return errors.New("network role runtime is not connected in this build")
	case config.RoleCombined:
		if configuration.Network != nil && configuration.Network.ListenerAddress != "" {
			return errors.New("network role runtime is not connected in this build")
		}
		return nil
	default:
		return errors.New("configured role is unsupported")
	}
}

type contextRunner interface {
	Run(context.Context) error
}

type idleRunner struct{}

func (idleRunner) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func waitRecoveryRetry(ctx context.Context, _ int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return nil
	}
}

type expiryRunner interface {
	Run(context.Context, time.Duration, sessionexpiry.ErrorReporter) error
}

type safeLogCleanupRunner interface {
	RunCleanup(context.Context, time.Duration) error
}

func runControllerWithMaintenance(
	ctx context.Context,
	controller contextRunner,
	expiry expiryRunner,
	cleanup safeLogCleanupRunner,
	input contextRunner,
	output contextRunner,
	statuses contextRunner,
	receipts contextRunner,
	supervision contextRunner,
	report sessionexpiry.ErrorReporter,
) error {
	if ctx == nil || controller == nil || expiry == nil || cleanup == nil || input == nil || output == nil || statuses == nil || receipts == nil || supervision == nil || report == nil {
		return errors.New("controller maintenance dependencies are required")
	}
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		component string
		err       error
	}
	results := make(chan result, 8)
	go func() {
		results <- result{component: "controller", err: controller.Run(runContext)}
	}()
	go func() {
		results <- result{component: "session expiry", err: expiry.Run(runContext, sessionExpiryInterval, report)}
	}()
	go func() {
		results <- result{component: "safe log cleanup", err: cleanup.RunCleanup(runContext, safeLogCleanupInterval)}
	}()
	go func() {
		results <- result{component: "durable input", err: input.Run(runContext)}
	}()
	go func() {
		results <- result{component: "durable output", err: output.Run(runContext)}
	}()
	go func() {
		results <- result{component: "Telegram status delivery", err: statuses.Run(runContext)}
	}()
	go func() {
		results <- result{component: "Telegram receipt reconciliation", err: receipts.Run(runContext)}
	}()
	go func() {
		results <- result{component: "session supervision", err: supervision.Run(runContext)}
	}()
	first := <-results
	cancel()
	<-results
	<-results
	<-results
	<-results
	<-results
	<-results
	<-results
	if first.component == "controller" {
		return first.err
	}
	if first.err == nil {
		return fmt.Errorf("%s stopped unexpectedly", first.component)
	}
	if errors.Is(first.err, context.Canceled) && ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("run %s: %w", first.component, first.err)
}

func loadEffectiveSettings(ctx context.Context, store *settings.FileStore) (settings.Effective, error) {
	if store == nil {
		return settings.Effective{}, errors.New("settings store is required")
	}
	snapshot, reloadErr := store.Reload(ctx)
	if reloadErr == nil {
		return snapshot.Settings.Effective(), nil
	}
	lastGood, err := store.Current(ctx)
	if err != nil {
		return settings.Effective{}, errors.Join(reloadErr, err)
	}
	return lastGood.Settings.Effective(), nil
}

func mapSessionLifetime(lifetime settings.SessionLifetime) (domain.SessionLifetime, error) {
	switch lifetime {
	case settings.LifetimeNever:
		return domain.SessionLifetimeNever, nil
	case settings.Lifetime6Hours:
		return domain.SessionLifetime6Hours, nil
	case settings.Lifetime12Hours:
		return domain.SessionLifetime12Hours, nil
	case settings.Lifetime24Hours:
		return domain.SessionLifetime24Hours, nil
	case settings.Lifetime48Hours:
		return domain.SessionLifetime48Hours, nil
	default:
		return 0, fmt.Errorf("unsupported session lifetime %q", lifetime)
	}
}

func configuredComputerID(configuration config.Config) (domain.ComputerID, error) {
	if configuration.IsLegacy() {
		return domain.ComputerID("local"), nil
	}
	if configuration.Computer == nil || configuration.Computer.ID == "" {
		return "", errors.New("versioned computer identity is required")
	}
	return domain.ComputerID(configuration.Computer.ID), nil
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
