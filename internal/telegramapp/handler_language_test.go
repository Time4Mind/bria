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
