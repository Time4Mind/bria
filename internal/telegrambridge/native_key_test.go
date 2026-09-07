package telegrambridge_test

import (
	"bria/internal/callbacktoken"
	"bria/internal/telegramui"
	"testing"
	"time"
)

func TestNativeKeySignsOnlyEightBoundedSessionKeys(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec := mustCallbackCodec(t, func() time.Time { return now })
	presenter := mustPresenter(t, codec, func() time.Time { return now }, time.Minute)
	labels := []string{"↑", "↓", "←", "→", "Enter", "Esc", "Tab", "Space"}
	for choice := 0; choice <= 9; choice++ {
		label := "invalid"
		if choice >= 1 && choice <= 8 {
			label = labels[choice-1]
		}
		button := telegramui.Button{Action: telegramui.ActionNativeKey, Label: label, Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: choice}}
		markup, err := presenter.PresentKeyboard(telegramui.GlobalSurfaceID, []string{testLogicalSessionID}, oneButton(button))
		if choice == 0 || choice == 9 {
			if err == nil {
				t.Fatalf("accepted key %d", choice)
			}
			if _, err := codec.Encode(callbacktoken.Fields{Action: callbacktoken.ActionNativeKey, SessionID: testLogicalSessionID, Target: choice, ExpiresAt: now.Add(time.Minute)}); err == nil {
				t.Fatalf("codec accepted key %d", choice)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out := markup.InlineKeyboard[0][0]
		decoded, err := presenter.DecodeCallback(out.CallbackData)
		if err != nil || decoded.Action != telegramui.ActionNativeKey || decoded.SessionID != testLogicalSessionID || decoded.Target.Choice != choice || out.Text != label {
			t.Fatalf("key=%#v err=%v", decoded, err)
		}
	}
}
