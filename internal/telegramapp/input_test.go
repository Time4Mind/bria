package telegramapp_test

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func (c *blockingControls) appendTranscriptEvent(event transcript.Event) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}

func notifyTest(target chan struct{}) {
	if target != nil {
		select {
		case target <- struct{}{}:
		default:
		}
	}
}

func waitTestNotification(t *testing.T, target chan struct{}, failure string) {
	t.Helper()
	select {
	case <-target:
	case <-time.After(4 * time.Second):
		t.Fatal(failure)
	}
}

func TestMediaDescriptorIsQueuedWithoutDownloadingOnTelegramLeader(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	err = handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 44, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "[forwarded from @helper_bot]\ninspect this",
		Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingDocument, FileID: "file-id", FileUniqueID: "unique-id",
			FileName: "report.pdf", MIMEType: "application/pdf", FileSize: 1024,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if controls.external == nil || controls.external.Kind != runtimehost.InputDocument ||
		controls.external.File.ID != "file-id" ||
		controls.external.Caption != "[forwarded from @helper_bot]\ninspect this" {
		t.Fatalf("external descriptor=%#v", controls.external)
	}
	if len(fixture.messenger.sent) != 1 || fixture.messenger.sent[0].Name != telegramui.ScreenSessionCard {
		t.Fatalf("sent screens=%#v", fixture.messenger.sent)
	}
}

func TestCurrentCardDisplayPreferencesDoNotBlockTextDelivery(t *testing.T) {
	fixture := newFixture(t)
	preferences := fixture.machine.State().Preferences[7]
	preferences.ResponseCards = domain.ResponseCardsKeepLatest
	preferences.HiddenCardEvents = []domain.CardEventType{
		domain.CardEventToolCall, domain.CardEventToolResult,
	}
	preferences.TerminalSnapshots = domain.TerminalSnapshotAlways
	if err := fixture.service.SetPreferences(
		context.Background(), application.Principal{UserID: 7}, preferences,
	); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 45, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Text: "must still be delivered",
	}); err != nil {
		t.Fatal(err)
	}
	if controls.text != "must still be delivered" {
		t.Fatalf("display preferences affected delivery: controls=%#v", controls)
	}
	if len(fixture.messenger.sent) != 1 || fixture.messenger.sent[0].Name != telegramui.ScreenSessionCard {
		t.Fatalf("sent screens=%#v", fixture.messenger.sent)
	}
}
