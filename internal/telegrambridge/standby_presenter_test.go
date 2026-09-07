package telegrambridge_test

import (
	"testing"
	"time"

	"bria/internal/telegrambridge"
	"bria/internal/telegramui"
)

func TestStandbySignedCallbackRoundTripAndTampering(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	markup, err := presenter.PresentKeyboard(telegramui.GlobalSurfaceID, nil, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Action: telegramui.ActionSettingsStandby, Label: "Ожидающая сессия"}}}})
	if err != nil {
		t.Fatal(err)
	}
	button := markup.InlineKeyboard[0][0]
	if button.Text != "Ожидающая сессия" {
		t.Fatalf("label=%q", button.Text)
	}
	got, err := presenter.DecodeCallback(button.CallbackData)
	want := telegrambridge.Callback{SessionID: telegramui.GlobalSurfaceID, Action: telegramui.ActionSettingsStandby}
	if err != nil || got != want {
		t.Fatalf("callback=%#v error=%v", got, err)
	}
	tampered := []byte(button.CallbackData)
	if tampered[10] == 'A' {
		tampered[10] = 'B'
	} else {
		tampered[10] = 'A'
	}
	if _, err := presenter.DecodeCallback(string(tampered)); err == nil {
		t.Fatal("tampered standby signature accepted")
	}
}
