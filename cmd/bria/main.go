package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"bria/internal/app"
	"bria/internal/config"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/instancelock"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
	"bria/internal/singlemachinecomposition"
	"bria/internal/storage"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramnotify"
)

var version = "dev"

type instanceLock interface{ Close() error }
type providerRuntime interface {
	app.SessionStarter
	sessionruntime.Submitter
}

type commandDependencies struct {
	acquireLock    func(string) (instanceLock, error)
	executable     func() (string, error)
	environment    func() []string
	telegramHTTP   func() telegram.HTTPClient
	composeRuntime func(config.Config, []string, string, sessionruntime.Options) (providerRuntime, error)
}

func toDependencies(deps commandDependencies) singlemachinecomposition.Dependencies {
	return singlemachinecomposition.Dependencies{
		AcquireLock: func(path string) (singlemachinecomposition.InstanceLock, error) { return deps.acquireLock(path) },
		Executable:  deps.executable, Environment: deps.environment, TelegramHTTP: deps.telegramHTTP,
		ComposeRuntime: func(c config.Config, e []string, x string, o sessionruntime.Options) (singlemachinecomposition.ProviderRuntime, error) {
			return deps.composeRuntime(c, e, x, o)
		},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}
func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runContextWithDependencies(ctx, args, stdout, stderr, productionDependencies())
}
func runContextWithDependencies(ctx context.Context, args []string, stdout, stderr io.Writer, deps commandDependencies) int {
	if len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help") {
		printHelp(stdout)
		return 0
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintf(stdout, "bria %s\n", version)
		return 0
	}
	if len(args) == 3 && args[1] == "--config" {
		if !filepath.IsAbs(args[2]) {
			fmt.Fprintln(stderr, "bria: configuration path must be absolute")
			return 2
		}
		var err error
		switch args[0] {
		case "run":
			err = singlemachinecomposition.Run(ctx, args[2], toDependencies(deps))
		case "check-config":
			err = checkConfig(args[2], deps)
		case "check-telegram":
			err = checkTelegram(ctx, args[2], deps)
		default:
			fmt.Fprintln(stderr, "bria: unknown command")
			return 2
		}
		if err != nil {
			if args[0] == "run" && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				return 0
			}
			fmt.Fprintf(stderr, "bria: %s: %v\n", args[0], err)
			return 1
		}
		if args[0] == "check-config" {
			fmt.Fprintln(stdout, "Bria configuration: OK")
		}
		if args[0] == "check-telegram" {
			fmt.Fprintln(stdout, "Telegram identity: OK")
		}
		return 0
	}
	fmt.Fprintln(stderr, "bria: unknown command")
	return 2
}

func checkConfig(configPath string, dependencies commandDependencies) error {
	configuration, err := config.LoadFile(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if configuration.Coordinates() {
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
	}
	if !configuration.Executes() {
		return nil
	}
	if dependencies.executable == nil || dependencies.environment == nil || dependencies.composeRuntime == nil {
		return errors.New("runtime composition dependencies are required")
	}
	executable, err := dependencies.executable()
	if err != nil {
		return errors.New("resolve Bria executable")
	}
	if _, err := dependencies.composeRuntime(configuration, dependencies.environment(), executable, sessionruntime.Options{}); err != nil {
		return fmt.Errorf("compose provider runtime: %w", err)
	}
	return nil
}

func checkTelegram(ctx context.Context, configPath string, dependencies commandDependencies) error {
	configuration, err := config.LoadFile(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if !configuration.Coordinates() {
		return errors.New("configured role does not own Telegram coordination")
	}
	if dependencies.telegramHTTP == nil {
		return errors.New("Telegram HTTP dependency is required")
	}
	token, err := configuration.ReadToken()
	if err != nil {
		return fmt.Errorf("read Telegram token: %w", err)
	}
	defer clear(token)
	client, err := telegram.NewClient(string(token), dependencies.telegramHTTP(), telegram.Options{})
	if err != nil {
		return fmt.Errorf("create Telegram client: %w", err)
	}
	readiness, err := telegrambridge.NewReadiness(client, configuration.BotUsername)
	if err != nil {
		return fmt.Errorf("create Telegram readiness check: %w", err)
	}
	return readiness.Ready(ctx, coordinator.Checkpoint{})
}
func productionDependencies() commandDependencies {
	return commandDependencies{acquireLock: func(path string) (instanceLock, error) { return instancelock.Acquire(path) }, executable: os.Executable, environment: os.Environ, telegramHTTP: telegram.NewProductionHTTPClient, composeRuntime: composeProviderRuntime}
}
func composeProviderRuntime(c config.Config, e []string, x string, o sessionruntime.Options) (providerRuntime, error) {
	return singlemachinecomposition.ComposeProviderRuntime(c, e, x, o)
}

type unavailableProviderRuntime struct{}

func (unavailableProviderRuntime) Start(context.Context, app.StartSessionRequest) (domain.ProviderBinding, error) {
	return domain.ProviderBinding{}, errors.New("no provider is enabled")
}
func (unavailableProviderRuntime) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	return errors.New("no provider is enabled")
}
func (unavailableProviderRuntime) Submit(context.Context, domain.SessionID, string) (sessionruntime.TurnResult, error) {
	return sessionruntime.TurnResult{}, errors.New("no provider is enabled")
}
func printHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `bria - Codex and Claude Telegram controller

Owner-only private commands:
  /status
  /new codex|claude /absolute/workdir
  /sessions
  /use SESSION_ID
  /stop

Current authorization limitations: OAuth/subscription login is not supported;
the live provider authorization smoke test has not been run.

Usage:
  bria [help|--help]
  bria version|--version
  bria run --config /absolute/path/to/config.json
  bria check-config --config /absolute/path/to/config.json
  bria check-telegram --config /absolute/path/to/config.json
`)
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

type replyRouteRecorder struct {
	store *storage.TelegramReplyRouteStore
}

func (recorder replyRouteRecorder) RecordOutboundReceipt(ctx context.Context, receipt telegramnotify.OutboundReceipt) error {
	if recorder.store == nil {
		return errors.New("Telegram reply route store is required")
	}
	return recorder.store.RecordOutboundReceipt(ctx, storage.TelegramOutboundReceipt{MessageID: receipt.MessageID, SessionID: receipt.SessionID})
}
