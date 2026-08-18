package telegramapp_test

import (
	"context"
	"slices"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestTamperedCallbackCannotResolveHiddenEntity(t *testing.T) {
	fixture := newFixture(t)
	hiddenToken, err := fixture.codec.Node(7, telegramui.ActionSelectNode, "hidden")
	if err != nil {
		t.Fatal(err)
	}
	data, err := (telegramui.Callback{Action: telegramui.ActionSelectNode, Token: hiddenToken}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 4, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "callback", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 0 || fixture.machine.State().Navigation.ActiveNodeByUser[7] != "allowed" {
		t.Fatal("tampered callback changed or exposed state")
	}
}

func TestSettingsCallbacksReplicateClosedPreferenceChoices(t *testing.T) {
	fixture := newFixture(t)
	callbacks := []telegramui.Callback{
		{Action: telegramui.ActionSetSessionView, Token: "all_hosts"},
		{Action: telegramui.ActionSetResumeSelection, Token: "off"},
		{Action: telegramui.ActionSetToolCalls, Token: "off"},
		{Action: telegramui.ActionSetToolResults, Token: "off"},
		{Action: telegramui.ActionSetToolOutputLines, Token: "25"},
		{Action: telegramui.ActionSetThinking, Token: "off"},
		{Action: telegramui.ActionSetResponseCards, Token: "keep_latest"},
		{Action: telegramui.ActionSetIdleArchive, Token: "unlimited"},
		{Action: telegramui.ActionSetRetention, Token: "30"},
		{Action: telegramui.ActionSetExpiry, Token: "all"},
	}
	for index, callback := range callbacks {
		data, err := callback.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
			UpdateID: int64(20 + index), Kind: telegrambot.IncomingCallback,
			ChatID: 7, UserID: 7, CallbackID: "settings", CallbackData: data,
			CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
		}); err != nil {
			t.Fatal(err)
		}
	}
	preferences := fixture.machine.State().Preferences[7]
	if preferences.SessionView != domain.ViewAllHosts || preferences.IdleArchiveHours != 0 ||
		preferences.ArchiveRetentionDays != 30 || preferences.ArchiveExpiryAction != domain.ArchiveRemoveAll ||
		preferences.ResponseCards != domain.ResponseCardsKeepLatest || !preferences.SkipResumeSelection {
		t.Fatalf("preferences=%#v", preferences)
	}
	if preferences.ShowsAllTechnicalCardEvents() || len(preferences.HiddenCardEvents) != 3 {
		t.Fatalf("technical card visibility=%#v", preferences.HiddenCardEvents)
	}
	if preferences.EffectiveToolOutputLines() != 25 {
		t.Fatalf("tool output lines=%d", preferences.EffectiveToolOutputLines())
	}
	if len(fixture.messenger.edited) != len(callbacks) ||
		fixture.messenger.edited[len(callbacks)-1].Name != telegramui.ScreenSettings {
		t.Fatalf("settings edits=%#v", fixture.messenger.edited)
	}
}

func TestSettingsCallbackStopsTelegramSpinnerBeforeRaftApply(t *testing.T) {
	fixture := newFixture(t)
	*fixture.events = (*fixture.events)[:0]
	data, err := (telegramui.Callback{
		Action: telegramui.ActionSetToolCalls, Token: "off",
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 39, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "settings", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"answer", "apply", "edit", "apply"}
	if got := *fixture.events; !slices.Equal(got, want) {
		t.Fatalf("callback event order=%v, want %v", got, want)
	}
}
