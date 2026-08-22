package telegramapp_test

import (
	"context"
	"errors"
	"fmt"
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
	if len(fixture.messenger.sent) != 1 ||
		!strings.Contains(fixture.messenger.sent[0].Text, "voice message was not sent") {
		t.Fatalf("disabled voice explanation=%#v", fixture.messenger.sent)
	}
	if grid := telegramui.CanonicalGrid(fixture.messenger.sent[0].Grid); !strings.Contains(grid, "voice_enable_yes") {
		t.Fatalf("disabled voice setup actions=%s", grid)
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
	if len(fixture.messenger.sent) != 1 || len(fixture.messenger.deleted) != 0 {
		t.Fatalf("voice refresh recreated carrier: sent=%#v deleted=%#v",
			fixture.messenger.sent, fixture.messenger.deleted)
	}
	latest := fixture.messenger.edited[len(fixture.messenger.edited)-1].Text
	if strings.Contains(latest, "Голосовое распознаётся") ||
		!strings.Contains(latest, "👤 распознанный запрос") {
		t.Fatalf("transcribed voice did not replace pending row: %q", latest)
	}
}

func TestVoiceBaselineIdentitySeparatesPromptsWithEqualRecentUserCounts(t *testing.T) {
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
	if err := fixture.service.PublishSessionRuntime(
		application.WithOperationScope(context.Background(), "voice-baseline-running"),
		session, domain.RuntimeRunning, nil,
	); err != nil {
		t.Fatal(err)
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "old bounded prompt",
		Timestamp: time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	sendVoice := func(updateID int64) runtimehost.InputPayload {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
			UpdateID: updateID, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
			Content: telegrambot.ContentDescriptor{
				Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileUniqueID: fmt.Sprintf("voice-%d", updateID),
			},
		}); err != nil {
			t.Fatal(err)
		}
		controls.mu.RLock()
		defer controls.mu.RUnlock()
		if controls.external == nil {
			t.Fatal("voice was not submitted")
		}
		return *controls.external
	}
	first := sendVoice(503)
	controls.mu.Lock()
	controls.events = []transcript.Event{{
		Kind: transcript.EventUserText, Text: "first recognized prompt",
		Timestamp: time.Now().Format(time.RFC3339Nano),
	}}
	controls.external = nil
	controls.mu.Unlock()
	second := sendVoice(504)
	if first.TranscriptBaselineCount != second.TranscriptBaselineCount {
		t.Fatalf("test did not reproduce a bounded-count collision: first=%#v second=%#v", first, second)
	}
	if first.TranscriptOrdinal != 1 || second.TranscriptOrdinal != 1 {
		t.Fatalf("distinct prompt baselines share an ordinal: first=%d second=%d",
			first.TranscriptOrdinal, second.TranscriptOrdinal)
	}
}

func TestExpiredVoiceAcknowledgementDoesNotInflateNewTranscriptOrdinal(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.VoiceBackend = domain.VoiceAuto
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	staleAt := time.Now().Add(-11 * time.Minute).UTC()
	stale, err := clusterstate.NewCommand(
		"stale-voice-ack", clusterstate.CommandPublishSessionRuntime, staleAt,
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: 1, Phase: domain.RuntimeRunning,
			Result: &domain.SessionOperationResult{
				OperationID: "stale-voice", Action: domain.ActionSendInput,
				Status: domain.OperationSucceeded, InputKind: "voice",
				TranscriptBaselineKnown: true, TranscriptBaselineCount: 1,
				TranscriptOrdinal: 7,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(stale); result.Err() != nil {
		t.Fatal(result.Err())
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{{
		Kind: transcript.EventUserText, Text: "current bounded baseline",
		Timestamp: time.Now().Add(-time.Minute).Format(time.RFC3339Nano),
	}}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := handler.HandleTelegramUpdate(ctx, telegrambot.IncomingUpdate{
		UpdateID: 505, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		Content: telegrambot.ContentDescriptor{
			Kind: telegrambot.IncomingVoice, FileID: "voice-id", FileUniqueID: "voice-505",
		},
	}); err != nil {
		t.Fatal(err)
	}
	controls.mu.RLock()
	defer controls.mu.RUnlock()
	if controls.external == nil || controls.external.TranscriptOrdinal != 1 {
		t.Fatalf("new voice inherited stale transcript ordinal: %#v", controls.external)
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

func TestInflatedDeliveredVoiceOrdinalsAreRepairedAfterHandlerRestart(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	acceptedAt := time.Now().UTC()
	baselineEvent := transcript.Event{
		Kind: transcript.EventUserText, Text: "before voices",
		Timestamp: acceptedAt.Add(-time.Minute).Format(time.RFC3339Nano),
	}
	for index := 1; index <= 2; index++ {
		session, err := fixture.service.Session(actor, ref)
		if err != nil {
			t.Fatal(err)
		}
		operationID := fmt.Sprintf("voice-restart-%d", index)
		queued := &domain.SessionOperationResult{
			OperationID: operationID, Action: domain.ActionSendInput,
			Status: domain.OperationQueued, InputKind: "voice",
			TranscriptBaselineKnown: true, TranscriptBaselineCount: 1,
			// Simulate acknowledgements persisted by the old matcher after it
			// counted an expired acknowledgement with the same bounded baseline.
			TranscriptOrdinal: index + 1,
		}
		if err := fixture.service.PublishSessionRuntime(
			application.WithOperationScope(context.Background(), operationID+"-queued"),
			session, domain.RuntimeRunning, queued,
		); err != nil {
			t.Fatal(err)
		}
		session, _ = fixture.service.Session(actor, ref)
		delivered := *queued
		delivered.Status = domain.OperationSucceeded
		if err := fixture.service.PublishSessionRuntime(
			application.WithOperationScope(context.Background(), operationID+"-delivered"),
			session, domain.RuntimeRunning, &delivered,
		); err != nil {
			t.Fatal(err)
		}
	}
	controls := &blockingControls{ref: ref, events: []transcript.Event{baselineEvent}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	selectSessionForVoiceTest(t, handler, fixture, ref, 501)
	latest := latestVoiceTestScreen(t, fixture)
	if count := strings.Count(latest, "recognized and sent"); count != 2 {
		t.Fatalf("durable voice acknowledgements after restart=%d screen=%q", count, latest)
	}
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventUserText, Text: "first recognized voice",
	})
	controls.appendTranscriptEvent(transcript.Event{
		Kind: transcript.EventUserText, Text: "second recognized voice",
	})
	selectSessionForVoiceTest(t, handler, fixture, ref, 502)
	latest = latestVoiceTestScreen(t, fixture)
	if strings.Contains(latest, "recognized and sent") ||
		!strings.Contains(latest, "first recognized voice") ||
		!strings.Contains(latest, "second recognized voice") {
		t.Fatalf("canonical transcript did not replace durable acknowledgements: %q", latest)
	}
}

func selectSessionForVoiceTest(
	t *testing.T,
	handler *telegramapp.Handler,
	fx fixture,
	ref domain.SessionRef,
	updateID int64,
) {
	t.Helper()
	token, err := fx.codec.Session(7, telegramui.ActionSelectSession, ref)
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
		UpdateID: updateID, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: fmt.Sprintf("voice-select-%d", updateID), CallbackData: callbackData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 1},
	}); err != nil {
		t.Fatal(err)
	}
}

func latestVoiceTestScreen(t *testing.T, fx fixture) string {
	t.Helper()
	if len(fx.messenger.edited) > 0 {
		return fx.messenger.edited[len(fx.messenger.edited)-1].Text
	}
	if len(fx.messenger.sent) > 0 {
		return fx.messenger.sent[len(fx.messenger.sent)-1].Text
	}
	t.Fatal("no voice test screen was rendered")
	return ""
}
