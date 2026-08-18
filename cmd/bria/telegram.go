package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/interaction"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func newTelegramAdapter(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	runtimeControl *nodeRuntimeControl,
	updateCoordinator *clusterupdate.Coordinator,
) (interaction.Adapter, error) {
	token, enabled, err := loadOptionalTelegramToken(nodeConfig.TelegramTokenFile)
	if err != nil || !enabled {
		return nil, err
	}
	codec, err := callbacktoken.LoadFile(nodeConfig.CallbackKeyFile)
	if err != nil {
		return nil, err
	}
	client, err := telegrambot.NewClient(telegrambot.ClientConfig{
		Token: token, ProxyURL: nodeConfig.TelegramProxyURL(),
	})
	if err != nil {
		return nil, err
	}
	service, err := application.NewService(node.State(), node)
	if err != nil {
		return nil, err
	}
	service.SetLeadership(node)
	adapterLeader := adapterLeadership{nodeID: domain.NodeID(nodeConfig.NodeID), node: node}
	caCertificate, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return nil, fmt.Errorf("read enrollment CA certificate: %w", err)
	}
	enrollmentAddress, err := nodeConfig.EnrollmentAdvertiseAddress()
	if err != nil {
		return nil, err
	}
	if err := service.SetEnrollmentInvitationConfig(application.EnrollmentInvitationConfig{
		ClusterID: nodeConfig.ClusterID, IssuerNodeID: nodeConfig.EffectiveEnrollmentIssuerID(),
		Endpoint: enrollmentAddress, CACertificate: string(caCertificate),
	}); err != nil {
		return nil, err
	}
	projector, err := application.NewTelegramProjector(node.State(), codec)
	if err != nil {
		return nil, err
	}
	projector.SetLeadership(node)
	if runtimeControl == nil {
		return nil, errors.New("node runtime control is required")
	}
	controls, err := sessioncontrol.NewWithTranscripts(
		service, runtimeControl.router, runtimeControl.transcripts,
	)
	if err != nil {
		return nil, err
	}
	if err := controls.SetSessionFiles(runtimeControl.sessionFiles); err != nil {
		return nil, err
	}
	if queued := controls.MigrateNames(); queued > 0 {
		fmt.Printf("bria session naming: queued_migrations=%d\n", queued)
	}
	handler, err := telegramapp.NewHandlerWithControlsAndLeadership(
		service, projector, codec, client, controls, adapterLeader,
	)
	if err != nil {
		return nil, err
	}
	starter, err := sessionstart.NewController(
		service, node.State(), runtimeControl.starts, adapterLeader,
	)
	if err != nil {
		return nil, err
	}
	if err := handler.SetSessionStarter(starter); err != nil {
		return nil, err
	}
	if err := handler.SetClusterAgentWorkdir(clusterAgentWorkdir(nodeConfig)); err != nil {
		return nil, err
	}
	if err := handler.SetProviderAuth(runtimeControl.providerAuth); err != nil {
		return nil, err
	}
	if err := handler.SetBackendSetup(runtimeControl.backendSetup); err != nil {
		return nil, err
	}
	if err := handler.SetSpeechSetup(runtimeControl.speechSetup); err != nil {
		return nil, err
	}
	if updateCoordinator != nil {
		if err := handler.SetClusterUpdater(updateCoordinator); err != nil {
			return nil, err
		}
	}
	go handler.RunInteractiveNotifications(ctx, 500*time.Millisecond)
	go handler.RunBackgroundNotifications(ctx, 1200*time.Millisecond)
	go handler.RunQuotaNotifications(ctx, 2*time.Second)
	go handler.RunEnrollmentNotifications(ctx, time.Second)
	go handler.RunClusterConnectivityNotifications(ctx, node, telegramapp.ClusterConnectivityConfig{
		LocalNodeID: domain.NodeID(nodeConfig.NodeID),
		Interval:    time.Second,
		LossGrace:   15 * time.Second,
	})
	go handler.RunClusterEventNotifications(ctx, node, telegramapp.ClusterConnectivityConfig{
		LocalNodeID: domain.NodeID(nodeConfig.NodeID),
		Interval:    time.Second,
	})
	go func() { _ = starter.Run(ctx) }()
	go func() { _ = controls.RunDeferredInputs(ctx, adapterLeader, 500*time.Millisecond) }()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = controls.Shutdown(shutdownCtx)
	}()
	observedHandler := telegrambot.UpdateHandlerFunc(func(
		updateCtx context.Context,
		update telegrambot.IncomingUpdate,
	) error {
		startedAt := time.Now()
		handleErr := handler.HandleTelegramUpdate(updateCtx, update)
		finishedAt := time.Now()
		outcome := "processed"
		if handleErr != nil {
			outcome = "failed"
		}
		fmt.Fprintf(os.Stderr, "bria telegram: update=%d kind=%s%s %s%s%s\n",
			update.UpdateID, update.Kind, telegramCallbackLogSuffix(update), outcome,
			telegramTimingLogSuffix(update, startedAt, finishedAt),
			telegramErrorSuffix(handleErr, token))
		return handleErr
	})
	cursor, err := application.NewReplicatedTelegramCursor(service)
	if err != nil {
		return nil, err
	}
	poller, err := telegrambot.NewPoller(telegrambot.PollerConfig{
		API: client, Leadership: adapterLeader, Cursor: cursor, Handler: observedHandler,
		LongPollTimeout: 30 * time.Second, LeadershipCheckInterval: 250 * time.Millisecond,
		RetryDelay: time.Second, MaxCallbackAttempts: 5,
		OnLeaderActivated: func(activationCtx context.Context) error {
			return activateTelegramLeader(activationCtx, client, service)
		},
		OnCallbackDropped: func(update telegrambot.IncomingUpdate, dropErr error, attempts int) {
			fmt.Fprintf(os.Stderr,
				"bria telegram: update=%d kind=%s%s dropped_after=%d%s\n",
				update.UpdateID, update.Kind, telegramCallbackLogSuffix(update), attempts,
				telegramErrorSuffix(dropErr, token),
			)
			noticeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			language := domain.LanguageFromTelegram(update.LanguageCode)
			actor := application.Principal{UserID: domain.UserID(update.UserID)}
			if preferences, preferencesErr := service.Preferences(actor); preferencesErr == nil {
				language = preferences.EffectiveLanguage()
			}
			failureNotice := i18n.For(string(language)).Text(i18n.CallbackFailed)
			if answerErr := client.AnswerCallbackQuery(
				noticeCtx, update.CallbackID, failureNotice,
			); answerErr != nil {
				fmt.Fprintf(os.Stderr,
					"bria telegram: update=%d callback_drop_notice_failed%s\n",
					update.UpdateID, telegramErrorSuffix(answerErr, token),
				)
			}
			// Most callbacks are acknowledged before their state or screen work.
			// A second callback answer then cannot show a toast, so send a visible
			// notice as well instead of silently losing the requested action.
			if _, noticeErr := client.SendMessage(noticeCtx, telegrambot.MessageRequest{
				ChatID: update.ChatID, Text: failureNotice,
			}); noticeErr != nil {
				fmt.Fprintf(os.Stderr,
					"bria telegram: update=%d callback_drop_message_failed%s\n",
					update.UpdateID, telegramErrorSuffix(noticeErr, token),
				)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return interaction.Func{AdapterName: "telegram", RunFunc: poller.Run}, nil
}

func clusterAgentWorkdir(nodeConfig config.Config) string {
	if current, err := os.Getwd(); err == nil {
		if _, statErr := os.Stat(filepath.Join(current, "go.mod")); statErr == nil {
			return current
		}
	}
	return nodeConfig.EffectiveUpdateInstallRoot()
}

func telegramTimingLogSuffix(
	update telegrambot.IncomingUpdate,
	startedAt time.Time,
	finishedAt time.Time,
) string {
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	suffix := fmt.Sprintf(" at=%s handle_ms=%d",
		finishedAt.UTC().Format(time.RFC3339Nano), finishedAt.Sub(startedAt).Milliseconds())
	if update.Kind == telegrambot.IncomingMessage && update.MessageDate > 0 {
		messageAt := time.Unix(update.MessageDate, 0)
		if !finishedAt.Before(messageAt) {
			suffix += fmt.Sprintf(" telegram_age_ms=%d", finishedAt.Sub(messageAt).Milliseconds())
		}
	}
	return suffix
}

// telegramCallbackLogSuffix identifies a failed UI route without persisting
// its signed opaque token. Tokens can reference cluster objects and must not
// become part of operational logs.
func telegramCallbackLogSuffix(update telegrambot.IncomingUpdate) string {
	if update.Kind != telegrambot.IncomingCallback {
		return ""
	}
	callback, err := telegramui.DecodeCallback(update.CallbackData)
	if err != nil {
		return fmt.Sprintf(" action=invalid card=%d", update.CallbackOrigin.MessageID)
	}
	return fmt.Sprintf(" action=%s card=%d", callback.Action, update.CallbackOrigin.MessageID)
}

func telegramErrorSuffix(err error, token string) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(err.Error(), token, "[redacted]")
	value = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		value = value[:240]
	}
	return fmt.Sprintf(" error=%T:%s", err, value)
}

func loadOptionalTelegramToken(path string) (string, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open Telegram token file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("inspect Telegram token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 512 {
		return "", false, errors.New("Telegram token file must be a small regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", false, errors.New("Telegram token file permissions must not allow group or other access")
	}
	data, err := io.ReadAll(io.LimitReader(file, 513))
	if err != nil {
		return "", false, fmt.Errorf("read Telegram token file: %w", err)
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false, errors.New("Telegram token file contains binary data")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", false, nil
	}
	return token, true, nil
}
