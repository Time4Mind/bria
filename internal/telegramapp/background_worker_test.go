package telegramapp_test

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestBackgroundReconciliationRestoresLiveCardWorkerAfterRestart(t *testing.T) {
	fixture := newFixture(t)
	fixture.messenger.sendNotify = make(chan struct{}, 1)
	fixture.messenger.editNotify = make(chan struct{}, 1)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	runningAt := time.Now().Add(-time.Second).UTC()
	applyBackgroundCommand(t, fixture, "restart-running",
		clusterstate.CommandPublishSessionRuntime, runningAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning,
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "legacy-live-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 71, Rich: false, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{
		ref: ref, pane: []byte("provider is working\n"), events: []transcript.Event{{
			Kind: transcript.EventToolCall, Head: "Bash", Body: "echo working",
			Timestamp: runningAt.Format(time.RFC3339Nano),
		}},
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	}()
	select {
	case <-fixture.messenger.sendNotify:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("restart reconciliation did not promote and refresh the live card")
	}
	select {
	case <-fixture.messenger.editNotify:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("restored worker did not refresh the terminal snapshot")
	}
	cancel()
	<-done
	if len(fixture.messenger.sent) == 0 || !fixture.messenger.sent[0].RichMarkdown {
		t.Fatalf("replacement screen = %#v", fixture.messenger.sent)
	}
	if len(fixture.messenger.edited) == 0 || fixture.messenger.edited[len(fixture.messenger.edited)-1].Pane == nil {
		t.Fatalf("refreshed screen = %#v", fixture.messenger.edited)
	}
	if len(fixture.messenger.deleted) == 0 || fixture.messenger.deleted[0].MessageID != 71 {
		t.Fatalf("legacy carrier was not replaced: %#v", fixture.messenger.deleted)
	}
}

func TestServersNavigationIsNotReplacedByRunningSessionRefresh(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	runningAt := time.Now().UTC()
	applyBackgroundCommand(t, fixture, "servers-navigation-running",
		clusterstate.CommandPublishSessionRuntime, runningAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: session.RuntimeGeneration,
			Phase: domain.RuntimeRunning,
		})
	running := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "servers-navigation-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 91, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventToolCall, Head: "Bash", Body: "still working",
		Timestamp: runningAt.Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 91, Rich: true}
	invokeCarrierAction(t, handler, 601, origin, telegramui.ActionSessions, "servers")
	if got := fixture.messenger.edited[len(fixture.messenger.edited)-1].Name; got != telegramui.ScreenStatus {
		t.Fatalf("navigation screen = %q", got)
	}
	card, ok, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) {
		t.Fatalf("navigation card checkpoint = %#v / %v / %v", card, ok, cardErr)
	}
	// Return to the live card, then open the byte-for-byte identical Servers
	// screen again. Each click is a distinct causal mutation even though the
	// resulting empty checkpoint has the same fingerprint.
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "returned-session-card"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 91, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	invokeCarrierAction(t, handler, 602, origin, telegramui.ActionSessions, "servers")
	card, ok, cardErr = fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !ok || card.Session != (domain.SessionRef{}) {
		t.Fatalf("repeated navigation card checkpoint = %#v / %v / %v", card, ok, cardErr)
	}
	// Model a stale cross-worker checkpoint that lands after navigation. The
	// handler-local visible view is the final authority for outbound rendering:
	// a session worker must not flash over the Servers screen even temporarily.
	if err := fixture.service.RecordTelegramResponseCard(
		application.WithOperationScope(context.Background(), "late-session-checkpoint"), actor,
		domain.TelegramResponseCard{
			ChatID: 7, MessageID: 91, Rich: true, Session: ref,
			SessionRevision: running.Revision, SessionEventAt: running.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	edits := len(fixture.messenger.edited)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.edited) != edits {
		t.Fatalf("servers screen was replaced: %#v", fixture.messenger.edited[edits:])
	}
}
