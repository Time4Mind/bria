package telegramapp_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerstop"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/transcript"
)

type delayedSettlementApplier struct {
	machine *clusterstate.Machine
	once    sync.Once
}

func (a *delayedSettlementApplier) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	if command.Kind != clusterstate.CommandPublishSessionRuntime {
		return a.machine.Apply(command), nil
	}
	a.once.Do(func() {
		go func() {
			time.Sleep(250 * time.Millisecond)
			command.OperationID += "-heartbeat"
			a.machine.Apply(command)
		}()
	})
	return clusterstate.Result{}, errors.New("leadership changed before apply response")
}

func TestProviderStopImmediatelyPublishesFirstVerifiedFinalOnly(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005482"
	session := fixture.machine.State().Sessions[ref.Key()]
	boundAt := time.Now().Add(-2 * time.Second).UTC()
	applyBackgroundCommand(t, fixture, "bind-provider-stop-test",
		clusterstate.CommandBindProviderSession, boundAt,
		clusterstate.BindProviderSession{
			ActorID: 7, Session: ref, ExpectedRevision: session.Revision,
			ProviderID: providerID,
		})
	promptAt := boundAt.Add(time.Second)
	bound := fixture.machine.State().Sessions[ref.Key()]
	applyBackgroundCommand(t, fixture, "run-provider-stop-test",
		clusterstate.CommandPublishSessionRuntime, promptAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: bound.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Result: &domain.SessionOperationResult{
				OperationID: "provider-stop-input", Action: domain.ActionSendInput,
				Status: domain.OperationQueued,
			},
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "provider-stop-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 55, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	finalAt := promptAt.Add(500 * time.Millisecond)
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventUserText, Text: "prompt", Timestamp: promptAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "EVENT DRIVEN FINAL", Timestamp: finalAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventTurnComplete, Timestamp: finalAt.Add(time.Millisecond).Format(time.RFC3339Nano)},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	bus := providerstop.NewBus(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handler.RunProviderStopNotifications(ctx, bus.Events())

	started := time.Now()
	signal := providerstop.Signal{
		NodeID: "allowed", SessionID: "live", ProviderSessionID: providerID,
	}
	if err := bus.Notify(ctx, signal); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(750 * time.Millisecond)
	for {
		sent, _, _ := fixture.messenger.screensSnapshot()
		if len(sent) == 1 {
			if !strings.Contains(sent[0].Text, "EVENT DRIVEN FINAL") {
				t.Fatalf("final screen=%q", sent[0].Text)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("verified provider final waited for periodic refresh")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("event-driven final latency=%v", elapsed)
	}
	card, ok, err := fixture.service.TelegramResponseCard(actor)
	if err != nil || !ok || !card.RenderedFinalAt.Equal(finalAt) {
		t.Fatalf("final card=%#v present=%v err=%v", card, ok, err)
	}

	if err := bus.Notify(ctx, signal); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	sent, edited, _ := fixture.messenger.screensSnapshot()
	if len(sent) != 1 || len(edited) != 0 {
		t.Fatalf("duplicate final produced sends=%d edits=%d", len(sent), len(edited))
	}
}

func TestProviderStopWaitsForConcurrentHeartbeatSettlement(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.events = nil
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	providerID := "019fffe8-02ee-7aa1-b6cf-eed13a005483"
	session := fixture.machine.State().Sessions[ref.Key()]
	boundAt := time.Now().Add(-2 * time.Second).UTC()
	applyBackgroundCommand(t, fixture, "bind-delayed-provider-stop",
		clusterstate.CommandBindProviderSession, boundAt,
		clusterstate.BindProviderSession{
			ActorID: 7, Session: ref, ExpectedRevision: session.Revision,
			ProviderID: providerID,
		})
	promptAt := boundAt.Add(time.Second)
	bound := fixture.machine.State().Sessions[ref.Key()]
	applyBackgroundCommand(t, fixture, "run-delayed-provider-stop",
		clusterstate.CommandPublishSessionRuntime, promptAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: bound.RuntimeGeneration,
			Phase: domain.RuntimeRunning, Result: &domain.SessionOperationResult{
				OperationID: "delayed-provider-stop-input", Action: domain.ActionSendInput,
				Status: domain.OperationQueued,
			},
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "delayed-provider-stop-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 56, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	finalAt := promptAt.Add(500 * time.Millisecond)
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventUserText, Text: "prompt", Timestamp: promptAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventAssistantFinal, Text: "FINAL AFTER HEARTBEAT RACE", Timestamp: finalAt.Format(time.RFC3339Nano)},
		{Kind: transcript.EventTurnComplete, Timestamp: finalAt.Add(time.Millisecond).Format(time.RFC3339Nano)},
	}}
	port := machinePort{machine: fixture.machine}
	service, err := application.NewService(port, &delayedSettlementApplier{machine: fixture.machine})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := telegramapp.NewHandlerWithControls(
		service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	bus := providerstop.NewBus(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handler.RunProviderStopNotifications(ctx, bus.Events())
	if err := bus.Notify(ctx, providerstop.Signal{
		NodeID: "allowed", SessionID: "live", ProviderSessionID: providerID,
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(1200 * time.Millisecond)
	for {
		sent, _, _ := fixture.messenger.screensSnapshot()
		if len(sent) == 1 {
			if !strings.Contains(sent[0].Text, "FINAL AFTER HEARTBEAT RACE") {
				t.Fatalf("final screen=%q", sent[0].Text)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("provider stop exhausted retries before heartbeat settlement")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
