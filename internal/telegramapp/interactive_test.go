package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/interactive"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (c *blockingControls) SendKey(
	_ context.Context,
	_ application.Principal,
	_ string,
	_ domain.SessionRef,
	key runtimehost.InteractiveKey,
	hash string,
) ([]byte, error) {
	c.key, c.keyHash = key, hash
	return append([]byte(nil), c.pane...), nil
}

func TestSelectingWaitingSessionAutomaticallyOpensKeyboard(t *testing.T) {
	fixture := newFixture(t)
	pane := []byte("Would you like to run this command?\n› 1. Yes\nPress enter to confirm or esc to cancel\n")
	prompt := publishInteractivePrompt(t, fixture, pane)
	controls := &blockingControls{
		ref: domain.SessionRef{NodeID: "allowed", SessionID: "live"}, pane: pane,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, controls.ref)
	if err != nil {
		t.Fatal(err)
	}
	callback := encodeCallback(t, telegramui.ActionSelectSession, token)
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 81, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "select", CallbackData: callback,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 {
		t.Fatalf("edited=%d", len(fixture.messenger.edited))
	}
	grid := telegramui.CanonicalGrid(fixture.messenger.edited[0].Grid)
	if !strings.Contains(grid, "key_enter") || !strings.Contains(grid, "key_ctrlc") ||
		!strings.Contains(fixture.messenger.edited[0].Text, prompt.Content) {
		t.Fatalf("screen=%#v grid=%s", fixture.messenger.edited[0], grid)
	}
}

func TestNewCodexSessionUpdatePromptOpensVerticalKeyboard(t *testing.T) {
	fixture := newFixture(t)
	pane := []byte("✨\u200aUpdate available! 0.97.0 -> 0.104.0\n\n" +
		"› 1. Update now\n  2. Skip\n  3. Skip until next version\n\nPress enter to continue\n")
	prompt, ok := interactive.Detect(pane)
	if !ok || prompt.Kind != "codex_update" {
		t.Fatalf("prompt=%#v/%v", prompt, ok)
	}
	actor := application.Principal{UserID: 7}
	session, err := fixture.service.ActiveSession(actor)
	if err != nil {
		t.Fatal(err)
	}
	report := domain.InteractivePromptReport{
		SessionID: session.ID, Generation: session.RuntimeGeneration,
		Present: true, Kind: prompt.Kind, Hash: prompt.Hash,
	}
	heartbeat := commandForTest(
		t, "new-session-update-prompt", clusterstate.CommandPublishNodeHeartbeat,
		clusterstate.PublishNodeHeartbeat{
			NodeID: "allowed", BootID: "boot", Interactive: []domain.InteractivePromptReport{report},
		},
	)
	if result := fixture.machine.Apply(heartbeat); result.Err() != nil {
		t.Fatal(result.Err())
	}
	controls := &blockingControls{ref: session.Ref(), pane: pane}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Session(7, telegramui.ActionSelectSession, session.Ref())
	if err != nil {
		t.Fatal(err)
	}
	callback := encodeCallback(t, telegramui.ActionSelectSession, token)
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 83, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "select-update", CallbackData: callback,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	screen := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	grid := telegramui.CanonicalGrid(screen.Grid)
	for _, action := range []string{"key_up", "key_down", "key_esc", "key_enter"} {
		if !strings.Contains(grid, action) {
			t.Fatalf("missing %s in %s", action, grid)
		}
	}
	if strings.Contains(grid, "key_left") || strings.Contains(grid, "key_right") ||
		!strings.Contains(screen.Text, "Update available!") {
		t.Fatalf("update screen=%#v grid=%s", screen, grid)
	}
}

func TestInteractiveCallbackSendsBoundKeyAndPromptHash(t *testing.T) {
	fixture := newFixture(t)
	pane := []byte("☐ Choose\n❯ 1. First\nEnter to select\n")
	prompt := publishInteractivePrompt(t, fixture, pane)
	ref := domain.SessionRef{NodeID: "allowed", SessionID: "live"}
	controls := &blockingControls{ref: ref, pane: pane}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Interactive(7, telegramui.ActionKeyDown, ref, prompt.Hash)
	if err != nil {
		t.Fatal(err)
	}
	callback := encodeCallback(t, telegramui.ActionKeyDown, token)
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 82, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "down", CallbackData: callback,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if controls.key != runtimehost.KeyDown || controls.keyHash != prompt.Hash {
		t.Fatalf("key=%q hash=%q", controls.key, controls.keyHash)
	}
	if len(fixture.messenger.answers) != 1 || len(fixture.messenger.edited) != 1 {
		t.Fatalf("answers=%v edited=%d", fixture.messenger.answers, len(fixture.messenger.edited))
	}
}

func TestBackgroundPromptSendsOneShortNotification(t *testing.T) {
	fixture := newFixture(t)
	created := time.Unix(200, 0).UTC()
	add, err := clusterstate.NewCommand(
		"add-background", clusterstate.CommandAddSession, created, domain.Session{
			ID: "background", NodeID: "allowed", OwnerID: 7, Name: "Background",
			Backend: "codex", State: domain.SessionLive,
			RuntimePhase: domain.RuntimeRunning, CreatedAt: created, LiveSinceAt: created,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(add); result.Err() != nil {
		t.Fatal(result.Err())
	}
	pane := []byte("Would you like to proceed?\n  1. Yes\nEsc to cancel\n")
	prompt, ok := interactive.Detect(pane)
	if !ok {
		t.Fatal("test prompt was not detected")
	}
	heartbeat, err := clusterstate.NewCommand(
		"background-heartbeat", clusterstate.CommandPublishNodeHeartbeat, created.Add(time.Second),
		clusterstate.PublishNodeHeartbeat{
			NodeID: "allowed", BootID: "boot",
			Interactive: []domain.InteractivePromptReport{{
				SessionID: "background", Generation: 1, Present: true,
				Kind: prompt.Kind, Hash: prompt.Hash,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(heartbeat); result.Err() != nil {
		t.Fatal(result.Err())
	}
	handler, err := telegramapp.NewHandler(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	handler.RunBackgroundNotifications(ctx, 5*time.Millisecond)
	if len(fixture.messenger.sent) != 1 {
		t.Fatalf("notifications=%d", len(fixture.messenger.sent))
	}
	screen := fixture.messenger.sent[0]
	if !strings.Contains(screen.Text, "❓ [Background · Allowed] action required") ||
		!strings.Contains(telegramui.CanonicalGrid(screen.Grid), "session@") {
		t.Fatalf("notification=%#v", screen)
	}
	notice := fixture.machine.State().Navigation.BackgroundByUser[7]["allowed/background"]
	if !notice.Notified {
		t.Fatalf("notification delivery was not replicated: %#v", notice)
	}
}

func TestActivePromptRepaintsReplicatedLiveCardWithoutNotification(t *testing.T) {
	fixture := newFixture(t)
	pane := []byte("☐ Choose\n❯ 1. First\nEnter to select\n")
	publishInteractivePrompt(t, fixture, pane)
	actor := application.Principal{UserID: 7}
	if err := fixture.service.RecordTelegramResponseCard(
		context.Background(), actor, domain.TelegramResponseCard{ChatID: 7, MessageID: 55},
	); err != nil {
		t.Fatal(err)
	}
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
	if len(fixture.messenger.sent) != 0 || len(fixture.messenger.edited) != 1 {
		t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
	if grid := telegramui.CanonicalGrid(fixture.messenger.edited[0].Grid); !strings.Contains(grid, "key_enter") {
		t.Fatalf("grid=%s", grid)
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

func publishInteractivePrompt(t *testing.T, fixture fixture, pane []byte) interactive.Prompt {
	t.Helper()
	prompt, ok := interactive.Detect(pane)
	if !ok {
		t.Fatal("test prompt was not detected")
	}
	command, err := clusterstate.NewCommand(
		"interactive-heartbeat", clusterstate.CommandPublishNodeHeartbeat, time.Now(),
		clusterstate.PublishNodeHeartbeat{
			NodeID: "allowed", BootID: "boot",
			Interactive: []domain.InteractivePromptReport{{
				SessionID: "live", Generation: 1, Present: true,
				Kind: prompt.Kind, Hash: prompt.Hash,
			}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	return prompt
}

func encodeCallback(
	t *testing.T,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) string {
	t.Helper()
	value, err := (telegramui.Callback{Action: action, Token: token}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
