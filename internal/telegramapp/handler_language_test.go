package telegramapp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestFirstCallbackInfersLanguageBeforeRendering(t *testing.T) {
	fixture := newFixture(t)
	callback := telegramui.Callback{
		Action: telegramui.ActionSettingsCategory, Token: "interface",
	}
	data, err := callback.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 60, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		LanguageCode: "zh-CN", CallbackID: "open", CallbackData: data,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Preferences[7].Language; got != domain.LanguageChinese {
		t.Fatalf("language=%q", got)
	}
	if len(fixture.messenger.edited) != 1 ||
		!strings.Contains(fixture.messenger.edited[0].Text, "界面和语言") {
		t.Fatalf("localized callback screen=%#v", fixture.messenger.edited)
	}
}

func TestFirstMessagePersistsTelegramLanguageAndRendersLocalizedMenu(t *testing.T) {
	fixture := newFixture(t)
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 40, Kind: telegrambot.IncomingMessage, ChatID: 7, UserID: 7,
		LanguageCode: "ru-RU", Text: "/start",
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Preferences[7].Language; got != domain.LanguageRussian {
		t.Fatalf("language=%q", got)
	}
	if len(fixture.messenger.sent) != 1 ||
		!strings.HasPrefix(fixture.messenger.sent[0].Text, "Меню · активная: Live") {
		t.Fatalf("sent=%#v", fixture.messenger.sent)
	}
}

func TestSettingsNavigationAndLanguageSwitchStayInSameCard(t *testing.T) {
	fixture := newFixture(t)
	openCategory := telegramui.Callback{
		Action: telegramui.ActionSettingsCategory, Token: "interface",
	}
	openData, err := openCategory.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 50, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "open", CallbackData: openData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.messenger.edited) != 1 ||
		!strings.Contains(fixture.messenger.edited[0].Text, "Interface and language") {
		t.Fatalf("category edit=%#v", fixture.messenger.edited)
	}

	setLanguage := telegramui.Callback{Action: telegramui.ActionSetLanguage, Token: "ru"}
	languageData, err := setLanguage.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.handler.HandleTelegramUpdate(context.Background(), telegrambot.IncomingUpdate{
		UpdateID: 51, Kind: telegrambot.IncomingCallback, ChatID: 7, UserID: 7,
		CallbackID: "language", CallbackData: languageData,
		CallbackOrigin: telegrambot.Message{ChatID: 7, MessageID: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.machine.State().Preferences[7].Language; got != domain.LanguageRussian {
		t.Fatalf("language=%q", got)
	}
	last := fixture.messenger.edited[len(fixture.messenger.edited)-1]
	if !strings.HasPrefix(last.Text, "<b>Язык</b>\n") ||
		!strings.Contains(telegramui.CanonicalGrid(last.Grid), "• Русский") {
		t.Fatalf("localized setting=%#v", last)
	}
	if got := fixture.messenger.answers[len(fixture.messenger.answers)-1]; got != "language:" {
		t.Fatalf("callback answer=%q", got)
	}
}
