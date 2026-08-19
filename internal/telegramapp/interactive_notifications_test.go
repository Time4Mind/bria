package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestActivePromptPostsNewFullCardWithoutNotification(t *testing.T) {
	fixture := newFixture(t)
	pane := []byte("☐ Choose\n❯ 1. First\nEnter to select\n")
	publishInteractivePrompt(t, fixture, pane)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session := fixture.machine.State().Sessions[ref.Key()]
	if err := fixture.service.RecordTelegramResponseCard(
		context.Background(), actor, domain.TelegramResponseCard{
			ChatID: 7, MessageID: 55, Session: ref,
			SessionRevision: session.Revision, SessionEventAt: session.LastEventAt,
		},
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, pane: pane}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	handler.RunInteractiveNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 1 || len(fixture.messenger.edited) != 0 {
		t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
	if grid := telegramui.CanonicalGrid(fixture.messenger.sent[0].Grid); !strings.Contains(grid, "key_enter") {
		t.Fatalf("grid=%s", grid)
	}
	if strings.Contains(fixture.messenger.sent[0].Text, "action required") {
		t.Fatalf("lightweight notification leaked into full card: %q", fixture.messenger.sent[0].Text)
	}
	if !containsMessageID(fixture.messenger.cleared, 55) {
		t.Fatalf("previous carrier keyboard was not cleared: %#v", fixture.messenger.cleared)
	}
	card, exists, cardErr := fixture.service.TelegramResponseCard(actor)
	if cardErr != nil || !exists || card.MessageID != 1 || card.Session != ref {
		t.Fatalf("response card=%#v exists=%v err=%v", card, exists, cardErr)
	}

	// A daemon restart starts with an empty in-memory seen map. The replicated
	// screen fingerprint must still suppress a duplicate Telegram message.
	restarted, newErr := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if newErr != nil {
		t.Fatal(newErr)
	}
	restartCtx, restartCancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer restartCancel()
	restarted.RunInteractiveNotifications(restartCtx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("restart duplicated waiting-input carrier: sent=%d", len(fixture.messenger.sent))
	}
}

func TestActivePromptWithoutRecordedCardSendsKeyboardImmediately(t *testing.T) {
	fixture := newFixture(t)
	pane := []byte("✨ Update available! 0.97.0 -> 0.104.0\n" +
		"› 1. Update now\n  2. Skip\nPress enter to continue\n")
	publishInteractivePrompt(t, fixture, pane)
	controls := &blockingControls{
		ref: domain.SessionRef{NodeID: "allowed", SessionID: "live"}, pane: pane,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	handler.RunInteractiveNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 1 || len(fixture.messenger.edited) != 0 {
		t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
	if !strings.Contains(fixture.messenger.sent[0].Text, "─────") ||
		strings.Contains(fixture.messenger.sent[0].Text, "action required") {
		t.Fatalf("interactive card text=%q", fixture.messenger.sent[0].Text)
	}
	grid := telegramui.CanonicalGrid(fixture.messenger.sent[0].Grid)
	if !strings.Contains(grid, "key_up") || !strings.Contains(grid, "key_down") ||
		!strings.Contains(grid, "key_esc") || !strings.Contains(grid, "key_enter") {
		t.Fatalf("grid=%s", grid)
	}
	if _, exists, cardErr := fixture.service.TelegramResponseCard(
		application.Principal{UserID: 7},
	); cardErr != nil || !exists {
		t.Fatalf("response card exists=%v err=%v", exists, cardErr)
	}
}
