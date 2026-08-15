package telegramapp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/sessioncontrol"
	"github.com/Time4Mind/bria/internal/telegramapp"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

func TestCarrierFlowAllHostsRemoteSessionReturnsToLiveCard(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	preferences, err := fixture.service.Preferences(actor)
	if err != nil {
		t.Fatal(err)
	}
	preferences.SessionView = domain.ViewAllHosts
	if err := fixture.service.SetPreferences(context.Background(), actor, preferences); err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 501}
	invokeCarrierAction(t, fixture.handler, 501, origin, telegramui.ActionMenu, "")
	menu := lastEdited(t, fixture)
	if menu.Name != telegramui.ScreenMenu {
		t.Fatalf("menu=%#v", menu)
	}
	invokeCarrierData(t, fixture.handler, 502, origin,
		callbackForAction(t, menu, telegramui.ActionSessions))
	sessions := lastEdited(t, fixture)
	if sessions.Name != telegramui.ScreenSessionCard ||
		!strings.Contains(sessions.Text, "Live · Allowed") {
		t.Fatalf("sessions=%#v", sessions)
	}
	if len(fixture.messenger.sent) != 0 || len(fixture.messenger.edited) != 2 {
		t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
	}
}

type restoringControls struct {
	*blockingControls
	service *application.Service
}

func (c restoringControls) Restore(
	ctx context.Context,
	actor application.Principal,
	operationID string,
	ref domain.SessionRef,
) (sessioncontrol.Accepted, error) {
	session, err := c.service.Session(actor, ref)
	if err != nil {
		return sessioncontrol.Accepted{}, err
	}
	if err := c.service.RestoreSession(ctx, actor, session); err != nil {
		return sessioncontrol.Accepted{}, err
	}
	if err := c.service.SelectSession(ctx, actor, ref); err != nil {
		return sessioncontrol.Accepted{}, err
	}
	return sessioncontrol.Accepted{Session: ref}, nil
}

func TestCarrierFlowArchiveRestoreReturnsToLiveCard(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := addReadyArchive(t, fixture, actor)
	controls := restoringControls{
		blockingControls: &blockingControls{ref: ref}, service: fixture.service,
	}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 504}
	invokeCarrierAction(t, handler, 530, origin, telegramui.ActionArchive, "")
	invokeCarrierData(t, handler, 531, origin,
		callbackForAction(t, lastEdited(t, fixture), telegramui.ActionSelectArchive))
	inspect := lastEdited(t, fixture)
	invokeCarrierData(t, handler, 532, origin,
		callbackForAction(t, inspect, telegramui.ActionRestore))
	card := lastEdited(t, fixture)
	if card.Name != telegramui.ScreenSessionCard || !strings.Contains(card.Text, "Archived · Allowed") {
		t.Fatalf("restored card=%#v", card)
	}
	session := fixture.machine.State().Sessions[ref.Key()]
	if !session.IsLive() || !session.ResumePending || session.RuntimePhase != domain.RuntimeDegraded {
		t.Fatalf("restored state=%#v", session)
	}
}

func TestCarrierFlowArchiveInspectHistoryAndBack(t *testing.T) {
	fixture := newFixture(t)
	actor := application.Principal{UserID: 7}
	ref := addReadyArchive(t, fixture, actor)
	controls := &blockingControls{ref: ref, events: []transcript.Event{
		{Kind: transcript.EventAssistantFinal, Text: "older page"},
		{Kind: transcript.EventAssistantFinal, Text: "latest page"},
	}}
	handler, err := telegramapp.NewHandlerWithControls(
		fixture.service, fixture.projector, fixture.codec, fixture.messenger, controls,
	)
	if err != nil {
		t.Fatal(err)
	}
	origin := telegrambot.Message{ChatID: 7, MessageID: 502}
	invokeCarrierAction(t, handler, 510, origin, telegramui.ActionArchive, "")
	list := lastEdited(t, fixture)
	invokeCarrierData(t, handler, 511, origin,
		callbackForAction(t, list, telegramui.ActionSelectArchive))
	inspect := lastSent(t, fixture)
	if !strings.Contains(inspect.Text, "latest page") {
		t.Fatalf("inspect=%#v", inspect)
	}
	richOrigin := telegrambot.Message{ChatID: 7, MessageID: 506, Rich: true}
	invokeCarrierData(t, handler, 512, richOrigin,
		callbackForAction(t, inspect, telegramui.ActionArchiveHistory))
	history := lastEdited(t, fixture)
	if !strings.Contains(telegramui.CanonicalGrid(history.Grid), "history_prev") {
		t.Fatalf("history=%#v", history)
	}
	invokeCarrierData(t, handler, 513, richOrigin,
		callbackForAction(t, history, telegramui.ActionSelectArchive))
	if back := lastEdited(t, fixture); !strings.Contains(back.Text, "latest page") {
		t.Fatalf("inspect back=%#v", back)
	}
}

func TestCarrierFlowSettingsSelectsAllHostsAndReturns(t *testing.T) {
	fixture := newFixture(t)
	origin := telegrambot.Message{ChatID: 7, MessageID: 503}
	invokeCarrierAction(t, fixture.handler, 520, origin, telegramui.ActionSettings, "")
	root := lastEdited(t, fixture)
	invokeCarrierAction(t, fixture.handler, 521, origin,
		telegramui.ActionSettingsCategory, telegramui.OpaqueToken(telegramui.CategoryInterface))
	category := lastEdited(t, fixture)
	invokeCarrierAction(t, fixture.handler, 522, origin,
		telegramui.ActionOpenSetting, telegramui.OpaqueToken(telegramui.SettingSessionView))
	setting := lastEdited(t, fixture)
	if root.Name != telegramui.ScreenSettings || category.Name != telegramui.ScreenSettings ||
		setting.Name != telegramui.ScreenSettings {
		t.Fatalf("settings flow=%#v/%#v/%#v", root, category, setting)
	}
	invokeCarrierAction(t, fixture.handler, 523, origin,
		telegramui.ActionSetSessionView, "all_hosts")
	if fixture.machine.State().Preferences[7].SessionView != domain.ViewAllHosts {
		t.Fatal("all-hosts setting was not committed")
	}
	invokeCarrierAction(t, fixture.handler, 524, origin,
		telegramui.ActionSettingsCategory, telegramui.OpaqueToken(telegramui.CategoryInterface))
	if got := lastEdited(t, fixture); !strings.Contains(got.Text, "Interface and language") {
		t.Fatalf("category back=%#v", got)
	}
}

func TestCarrierFlowResponseCardModesStayOnSetting(t *testing.T) {
	tests := []struct {
		value string
		want  domain.ResponseCardMode
	}{
		{"keep_paginated", domain.ResponseCardsKeepPaginated},
		{"keep_latest", domain.ResponseCardsKeepLatest},
		{"replace_paginated", domain.ResponseCardsReplace},
	}
	for index, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			fixture := newFixture(t)
			origin := telegrambot.Message{ChatID: 7, MessageID: int64(600 + index)}
			invokeCarrierAction(t, fixture.handler, int64(600+index*10), origin,
				telegramui.ActionSettingsCategory, telegramui.OpaqueToken(telegramui.CategoryCard))
			invokeCarrierAction(t, fixture.handler, int64(601+index*10), origin,
				telegramui.ActionOpenSetting, telegramui.OpaqueToken(telegramui.SettingResponseCards))
			before := lastEdited(t, fixture)
			if !strings.Contains(telegramui.CanonicalGrid(before.Grid), "set_cards@"+test.value) {
				t.Fatalf("response choices=%#v", before)
			}
			invokeCarrierAction(t, fixture.handler, int64(602+index*10), origin,
				telegramui.ActionSetResponseCards, telegramui.OpaqueToken(test.value))
			after := lastEdited(t, fixture)
			if after.Name != telegramui.ScreenSettings ||
				fixture.machine.State().Preferences[7].ResponseCards != test.want {
				t.Fatalf("mode=%#v screen=%#v", fixture.machine.State().Preferences[7], after)
			}
			if len(fixture.messenger.sent) != 0 || len(fixture.messenger.edited) != 3 {
				t.Fatalf("sent=%d edited=%d", len(fixture.messenger.sent), len(fixture.messenger.edited))
			}
		})
	}
}

func invokeCarrierAction(
	t *testing.T,
	handler *telegramapp.Handler,
	updateID int64,
	origin telegrambot.Message,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) {
	t.Helper()
	invokeCarrierData(t, handler, updateID, origin, encodeCallback(t, action, token))
}

func invokeCarrierData(
	t *testing.T,
	handler *telegramapp.Handler,
	updateID int64,
	origin telegrambot.Message,
	data string,
) {
	t.Helper()
	if err := handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: updateID, Kind: telegrambot.IncomingCallback,
		UserID: 7, ChatID: 7, CallbackID: "carrier", CallbackData: data,
		CallbackOrigin: origin,
	}); err != nil {
		t.Fatal(err)
	}
}

func lastEdited(t *testing.T, fixture fixture) telegramui.Screen {
	t.Helper()
	if len(fixture.messenger.edited) == 0 {
		t.Fatal("no carrier edit")
	}
	return fixture.messenger.edited[len(fixture.messenger.edited)-1]
}

func lastSent(t *testing.T, fixture fixture) telegramui.Screen {
	t.Helper()
	if len(fixture.messenger.sent) == 0 {
		t.Fatal("no new carrier")
	}
	return fixture.messenger.sent[len(fixture.messenger.sent)-1]
}
