package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/interaction"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramview"
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
	projector, err := telegramview.New(node.State(), codec)
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
		processlog.Servicef("bria session naming: queued_migrations=%d", queued)
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
	go handler.RunProviderStopNotifications(ctx, runtimeControl.providerStops.Events())
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
		ingress, ingressErr := telegramInteractionIngress(update)
		if ingressErr != nil {
			return ingressErr
		}
		updateCtx = interaction.WithIngress(updateCtx, ingress)
		startedAt := time.Now()
		handleErr := handler.HandleTelegramUpdate(updateCtx, update)
		finishedAt := time.Now()
		if update.Kind == telegrambot.IncomingCallback {
			return handleErr
		}
		outcome := "processed"
		if handleErr != nil {
			outcome = "failed"
		}
		writeEvent := processlog.Detailf
		if handleErr != nil {
			processlog.Failuref(
				processlog.Service, telegramFailureClass(handleErr),
				"bria interaction: adapter=telegram interaction=%q kind=%s outcome=%s%s",
				ingress.ID(), update.Kind, outcome,
				telegramTimingLogSuffix(update, startedAt, finishedAt),
			)
			return handleErr
		}
		writeEvent("bria interaction: adapter=telegram interaction=%q kind=%s outcome=%s%s",
			ingress.ID(), update.Kind, outcome,
			telegramTimingLogSuffix(update, startedAt, finishedAt))
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
		OnCallbackProcessed: func(
			update telegrambot.IncomingUpdate, attempts int, recovered bool, duration time.Duration,
		) {
			ingress, ingressErr := telegramInteractionIngress(update)
			if ingressErr != nil {
				return
			}
			writeEvent := processlog.Detailf
			outcome := "processed"
			if recovered {
				writeEvent = processlog.Servicef
				outcome = "recovered"
			}
			writeEvent(
				"bria interaction: adapter=telegram interaction=%q kind=callback outcome=%s attempt=%d handle_ms=%d%s",
				ingress.ID(), outcome, attempts, duration.Milliseconds(),
				telegramCallbackLogSuffix(update),
			)
		},
		OnCallbackRetry: func(
			update telegrambot.IncomingUpdate, retryErr error, attempt, maximum int,
			duration time.Duration,
		) {
			ingress, ingressErr := telegramInteractionIngress(update)
			if ingressErr != nil {
				return
			}
			level := processlog.Detail
			if attempt == 1 {
				level = processlog.Service
			}
			processlog.Failuref(
				level, telegramFailureClass(retryErr),
				"bria interaction: adapter=telegram interaction=%q kind=callback outcome=retry_scheduled attempt=%d max_attempts=%d handle_ms=%d%s",
				ingress.ID(), attempt, maximum, duration.Milliseconds(),
				telegramCallbackLogSuffix(update),
			)
		},
		OnError: func(stage string, pollErr error) {
			processlog.Failuref(
				processlog.Critical, telegramFailureClass(pollErr),
				"bria telegram: poller stage=%s outcome=failed",
				stage,
			)
		},
		OnCallbackDropped: func(
			update telegrambot.IncomingUpdate, dropErr error, attempts int, duration time.Duration,
		) {
			ingress, ingressErr := telegramInteractionIngress(update)
			if ingressErr == nil {
				processlog.Failuref(
					processlog.Critical, telegramFailureClass(dropErr),
					"bria interaction: adapter=telegram interaction=%q kind=callback outcome=dropped attempts=%d terminal=true handle_ms=%d%s",
					ingress.ID(), attempts, duration.Milliseconds(), telegramCallbackLogSuffix(update),
				)
			}
			// The error is deliberately not logged here: the stable failure class
			// above is enough for diagnostics and cannot expose adapter payloads.
			_ = dropErr
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
				processlog.Failuref(
					processlog.Critical, telegramFailureClass(answerErr),
					"bria interaction: adapter=telegram interaction=%q kind=callback outcome=drop_notice_failed terminal=true",
					ingress.ID(),
				)
			}
			if _, limited := telegrambot.FloodWait(dropErr); limited {
				// The screen layer already records Telegram's requested cooldown.
				// Do not amplify it with a visible-message fallback that is certain
				// to hit the same per-chat flood control.
				return
			}
			// Most callbacks are acknowledged before their state or screen work.
			// A second callback answer then cannot show a toast, so send a visible
			// notice as well instead of silently losing the requested action.
			if _, noticeErr := client.SendMessage(noticeCtx, telegrambot.MessageRequest{
				ChatID: update.ChatID, Text: failureNotice,
			}); noticeErr != nil {
				processlog.Failuref(
					processlog.Critical, telegramFailureClass(noticeErr),
					"bria interaction: adapter=telegram interaction=%q kind=callback outcome=drop_message_failed terminal=true",
					ingress.ID(),
				)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	return interaction.Func{AdapterName: "telegram", RunFunc: poller.Run}, nil
}
