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
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestSpeechSetupWatcherReconcilesExistingAndNewNodes(t *testing.T) {
	fixture := newFixture(t)
	setup := &speechSetupStub{}
	if err := fixture.handler.SetSpeechSetup(setup); err != nil {
		t.Fatal(err)
	}
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.VoiceBackend = domain.VoiceAuto
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		fixture.handler.RunEnrollmentNotifications(ctx, 5*time.Millisecond)
	}()
	deadline := time.Now().Add(time.Second)
	for setup.requestCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if setup.requestCount() != 1 {
		t.Fatal("existing node was not reconciled after handler start")
	}
	newNode := domain.Node{ID: "new-node", Name: "New node", Status: domain.NodeOnline}
	if result := fixture.machine.Apply(commandForTest(
		t, "add-new-node", clusterstate.CommandAddNode, newNode,
	)); result.Err() != nil {
		t.Fatal(result.Err())
	}
	deadline = time.Now().Add(time.Second)
	for setup.requestCount() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if setup.requestCount() != 2 {
		t.Fatal("new node was not provisioned")
	}
	if len(fixture.messenger.sent) != 0 {
		t.Fatalf("automatic provisioning sent progress notifications=%#v", fixture.messenger.sent)
	}
	fixture.messenger.sendNotify = make(chan struct{}, 1)
	setup.setStatus(speechsetup.Status{
		NodeID: "new-node", Engine: "whisper", Phase: speechsetup.PhaseFailed,
		Detail: "ffmpeg is not installed", UpdatedAt: time.Now(),
	})
	select {
	case <-fixture.messenger.sendNotify:
	case <-time.After(time.Second):
		t.Fatal("terminal setup failure was not reported")
	}
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if len(fixture.messenger.sent) != 1 ||
		!strings.Contains(fixture.messenger.sent[0].Text, "ffmpeg is not installed") {
		t.Fatalf("terminal setup notifications=%#v", fixture.messenger.sent)
	}
}

func TestBackendSetupConnectionFailureIsRenderedWithItsCause(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.SetBackendSetup(failingBackendSetup{
		err: errors.New("relay connection unavailable"),
	}); err != nil {
		t.Fatal(err)
	}
	token, err := fixture.codec.Choice(
		7, telegramui.ActionBackendInstall, "node_backend", "allowed\x00codex",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 22, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID:     "backend-install",
		CallbackData:   encodeCallback(t, telegramui.ActionBackendInstall, token),
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
		LanguageCode:   "en",
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 || !strings.Contains(
		fixture.messenger.edited[0].Text, "relay connection unavailable",
	) {
		t.Fatalf("backend setup error screen=%#v", fixture.messenger.edited)
	}
	back := fixture.messenger.edited[0].Grid[len(fixture.messenger.edited[0].Grid)-1][0]
	if back.Callback.Action != telegramui.ActionNodeBackend {
		t.Fatalf("backend setup back action=%q", back.Callback.Action)
	}
}

func TestNodeBackendManagementUsesNestedScreens(t *testing.T) {
	fixture := newFixture(t)
	nodeToken, err := fixture.codec.Node(7, telegramui.ActionNodeBackends, "allowed")
	if err != nil {
		t.Fatal(err)
	}
	openToken, err := fixture.codec.Choice(
		7, telegramui.ActionNodeBackend, "node_backend_open", "allowed\x00codex",
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, callback := range []telegramui.Callback{
		{Action: telegramui.ActionNodeBackends, Token: nodeToken},
		{Action: telegramui.ActionNodeBackend, Token: openToken},
	} {
		if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: 30 + int64(index), Kind: telegrambot.IncomingCallback,
			UserID: 7, ChatID: 7, CallbackID: "backend-menu",
			CallbackData:   encodeCallback(t, callback.Action, callback.Token),
			CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
			LanguageCode:   "en",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.messenger.edited) != 2 {
		t.Fatalf("backend menu edits=%d", len(fixture.messenger.edited))
	}
	list, detail := fixture.messenger.edited[0], fixture.messenger.edited[1]
	if grid := telegramui.CanonicalGrid(list.Grid); !strings.Contains(grid,
		"[codex · not installed -> node_backend@") {
		t.Fatalf("backend list missing codex:\n%s", grid)
	}
	if grid := telegramui.CanonicalGrid(detail.Grid); !strings.Contains(grid,
		"[install -> backend_install@") || !strings.Contains(detail.Text, "Allowed · codex") {
		t.Fatalf("backend detail invalid:\n%s\n%s", detail.Text, grid)
	}
}
