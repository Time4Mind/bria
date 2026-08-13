package telegramapp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/speechsetup"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type speechSetupStub struct{ requests []speechsetup.Request }

func (s *speechSetupStub) Start(
	_ context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	s.requests = append(s.requests, request)
	return speechsetup.Status{
		NodeID: request.NodeID, Engine: "whisper", Phase: speechsetup.PhaseInstalling,
	}, nil
}

func (s *speechSetupStub) Status(
	_ context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	return speechsetup.Status{NodeID: request.NodeID, Engine: "whisper"}, nil
}

func TestBackgroundNotificationAndDismissSettingsAreReplicated(t *testing.T) {
	fixture := newFixture(t)
	applySettingCallback(t, fixture, 201, telegramui.ActionSetNotifyError, "off")
	preferences := fixture.machine.State().Preferences[7]
	if preferences.SendsBackgroundNotification(domain.BackgroundError) {
		t.Fatal("background errors remained enabled")
	}
	applySettingCallback(t, fixture, 202, telegramui.ActionSetBgDismiss, "5")
	preferences = fixture.machine.State().Preferences[7]
	if preferences.EffectiveBackgroundDismissSwitches() != 5 {
		t.Fatalf("dismiss switches=%d", preferences.EffectiveBackgroundDismissSwitches())
	}
}

func TestToolOutputLineSettingIsReplicated(t *testing.T) {
	fixture := newFixture(t)
	applySettingCallback(t, fixture, 200, telegramui.ActionSetToolOutputLines, "25")
	if got := fixture.machine.State().Preferences[7].EffectiveToolOutputLines(); got != 25 {
		t.Fatalf("tool output lines=%d", got)
	}
}

func TestVoiceBackendOffSettingIsReplicated(t *testing.T) {
	fixture := newFixture(t)
	preferences := fixture.machine.State().Preferences[7]
	preferences.VoiceBackend = domain.VoiceAuto
	command, err := clusterstate.NewCommand("voice-on", clusterstate.CommandSetPreferences,
		time.Now(), clusterstate.SetPreferences{UserID: 7, Preferences: preferences})
	if err != nil || fixture.machine.Apply(command).Err() != nil {
		t.Fatalf("seed voice preference: %v", err)
	}
	applySettingCallback(t, fixture, 207, telegramui.ActionSetVoiceBackend, "off")
	if got := fixture.machine.State().Preferences[7].EffectiveVoiceBackend(); got != domain.VoiceOff {
		t.Fatalf("voice backend=%q", got)
	}
}

func TestVoiceBackendOnRequiresConfirmation(t *testing.T) {
	fixture := newFixture(t)
	applySettingCallback(t, fixture, 208, telegramui.ActionSetVoiceBackend, "on")
	if got := fixture.machine.State().Preferences[7].EffectiveVoiceBackend(); got != domain.VoiceOff {
		t.Fatalf("voice enabled before confirmation: %q", got)
	}
	latest := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.Contains(latest.Text, "Enable speech recognition?") ||
		!strings.Contains(telegramui.CanonicalGrid(latest.Grid), "voice_enable_yes") {
		t.Fatalf("confirmation was not rendered: %#v", latest)
	}
}

func TestVoiceBackendConfirmationEnablesAndStartsVisibleNodes(t *testing.T) {
	fixture := newFixture(t)
	setup := &speechSetupStub{}
	if err := fixture.handler.SetSpeechSetup(setup); err != nil {
		t.Fatal(err)
	}
	applySettingCallback(t, fixture, 209, telegramui.ActionSetVoiceBackend, "on")
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 210, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "voice-confirm", CallbackData: encodeCallback(t, telegramui.ActionConfirmVoiceEnable, ""),
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10}, LanguageCode: "en",
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Preferences[7].EffectiveVoiceBackend(); got != domain.VoiceAuto {
		t.Fatalf("confirmed voice backend=%q requests=%#v edits=%d answers=%#v events=%#v",
			got, setup.requests, len(fixture.messenger.edited), fixture.messenger.answers, *fixture.events)
	}
	if len(setup.requests) != 1 || setup.requests[0].NodeID != "allowed" {
		t.Fatalf("speech setup requests=%#v", setup.requests)
	}
}

func TestProviderAccountAliasFlowReplicatesAndCanClear(t *testing.T) {
	fixture := newFixture(t)
	command, err := clusterstate.NewCommand(
		"publish-backend", clusterstate.CommandUpdateNodeRuntime, time.Now(),
		clusterstate.UpdateNodeRuntime{
			NodeID: "allowed", Status: domain.NodeOnline,
			Backends: []domain.BackendDescriptor{{Name: "codex"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := fixture.machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	value := "allowed\x00codex"
	token, err := fixture.codec.Choice(
		7, telegramui.ActionProviderAlias, "provider_alias", value,
	)
	if err != nil {
		t.Fatal(err)
	}
	data := encodeCallback(t, telegramui.ActionProviderAlias, token)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 205, Kind: telegrambot.IncomingCallback, UserID: 7, ChatID: 7,
		CallbackID: "alias", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) == 0 || !strings.Contains(
		fixture.messenger.edited[len(fixture.messenger.edited)-1].Text, "codex",
	) {
		t.Fatal("provider alias prompt was not rendered")
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 206, Kind: telegrambot.IncomingMessage, UserID: 7, ChatID: 7, Text: "Personal",
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().ProviderAccountAlias("allowed", "codex"); got != "Personal" {
		t.Fatalf("alias=%q", got)
	}
}

func TestClusterPollingAndNodeSortSettingsAreReplicated(t *testing.T) {
	fixture := newFixture(t)
	applySettingCallback(t, fixture, 203, telegramui.ActionSetNodeSort, "leader")
	applySettingCallback(t, fixture, 204, telegramui.ActionSetQuotaPoll, "5")
	preferences := fixture.machine.State().Preferences[7]
	if preferences.EffectiveNodeSort() != domain.NodeSortLeader ||
		preferences.EffectiveQuotaPollMinutes() != 5 {
		t.Fatalf("cluster preferences=%#v", preferences)
	}
}

func applySettingCallback(
	t *testing.T,
	fixture fixture,
	updateID int64,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) {
	t.Helper()
	data := encodeCallback(t, action, token)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback,
		UserID: 7, ChatID: 7, CallbackID: "setting", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
}
