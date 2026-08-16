package telegramapp_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
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
	case <-time.After(2 * time.Second):
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

func TestVoicePendingRowMatchesCCBotUntilTranscriptArrives(t *testing.T) {
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
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	session, err := fixture.service.Session(actor, ref)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx := application.WithOperationScope(context.Background(), "voice-test-running")
	if err := fixture.service.PublishSessionRuntime(
		runtimeCtx, session, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "предыдущий запрос",
		Timestamp: time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	fixture.messenger.editNotify = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 48, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileUniqueID: "voice-unique",
		},
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	const pending = "👤 🎙 Голосовое распознаётся…"
	if len(fixture.messenger.sent) != 1 || !strings.Contains(fixture.messenger.sent[0].Text, pending) {
		cancel()
		t.Fatalf("initial voice card=%#v", fixture.messenger.sent)
	}
	if strings.Contains(fixture.messenger.sent[0].Text, "Голосовое принято") {
		cancel()
		t.Fatalf("legacy voice notice remained: %q", fixture.messenger.sent[0].Text)
	}
	select {
	case <-fixture.messenger.editNotify:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("pending voice card was not refreshed")
	}
	cancel()
	if len(fixture.messenger.edited) == 0 ||
		!strings.Contains(fixture.messenger.edited[len(fixture.messenger.edited)-1].Text, pending) {
		t.Fatalf("pending marker disappeared before transcription: %#v", fixture.messenger.edited)
	}

	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventUserText, Text: "распознанный запрос",
		Timestamp: time.Now().Format(time.RFC3339Nano),
	})
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, ref)
	if err != nil {
		t.Fatal(err)
	}
	callbackData, err := (telegramui.Callback{
		Action: telegramui.ActionSelectSession, Token: token,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 49, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "select", CallbackData: callbackData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 1},
	}); err != nil {
		t.Fatal(err)
	}
	latest := fixture.messenger.sent[len(fixture.messenger.sent)-1].Text
	if strings.Contains(latest, "Голосовое распознаётся") ||
		!strings.Contains(latest, "👤 распознанный запрос") {
		t.Fatalf("transcribed voice did not replace pending row: %q", latest)
	}
}

func TestVoicePendingWithoutTranscriptStaysBeforeBackgroundPanel(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	created := time.Unix(90, 0).UTC()
	addBackground, err := clusterstate.NewCommand(
		"add-background-for-voice", clusterstate.CommandAddSession, created,
		domain.Session{
			ID: "background", NodeID: "allowed", OwnerID: 7, Name: "Background",
			Backend: "codex", State: domain.SessionLive, RuntimePhase: domain.RuntimeIdle,
			CreatedAt: created, LiveSinceAt: created,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(addBackground); result.Err() != nil {
		t.Fatal(result.Err())
	}
	if err := fixture.service.SelectSession(
		context.Background(), actor, domain.SessionRef{NodeID: "allowed", SessionID: "live"},
	); err != nil {
		t.Fatal(err)
	}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.Language = domain.LanguageRussian
	preferences.VoiceBackend = domain.VoiceAuto
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &transcriptErrorControls{
		blockingControls: &blockingControls{ref: ref}, err: errors.New("not created yet"),
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 50, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileUniqueID: "voice-unique",
		},
	}); err != nil {
		t.Fatal(err)
	}
	text := fixture.messenger.sent[len(fixture.messenger.sent)-1].Text
	pending := strings.Index(text, "👤 🎙 Голосовое распознаётся…")
	background := strings.Index(text, "фон")
	if pending < 0 || background < 0 || pending > background {
		t.Fatalf("pending voice row is outside active content:\n%s", text)
	}
}
