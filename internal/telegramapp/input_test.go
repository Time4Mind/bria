package telegramapp_test

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

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

func TestVoiceInputIsRejectedBeforeNodeTransferWhenRecognitionIsOff(t *testing.T) {
	fixture := newFixture(t)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 46, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileSize: 1024,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if controls.external != nil {
		t.Fatalf("disabled voice was transferred to a node: %#v", controls.external)
	}
}

func TestVoiceInputUsesTheExplicitInterfaceLanguage(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.Language = domain.LanguageRussian
	preferences.VoiceBackend = domain.VoiceAuto
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: domain.SessionRef{NodeID: "allowed", SessionID: "live"}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 47, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		LanguageCode: "en", Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileUniqueID: "voice-unique",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if controls.external == nil || controls.external.VoiceBackend != "auto" ||
		controls.external.VoiceLanguage != "ru" {
		t.Fatalf("voice routing metadata=%#v", controls.external)
	}
}
