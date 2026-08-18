package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerstop"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/transcript"
)

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
