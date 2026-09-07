package telegrambridge_test

import (
	"bria/internal/callbacktoken"
	"bria/internal/telegramui"
	"testing"
	"time"
)

func TestModelSelectorSignsExactSessionAndBoundedChoice(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	codec := mustCallbackCodec(t, func() time.Time { return now })
	presenter := mustPresenter(t, codec, func() time.Time { return now }, time.Minute)
	for _, action := range []telegramui.Action{telegramui.ActionModelMenu, telegramui.ActionModelChoice, telegramui.ActionEffortMenu, telegramui.ActionEffortChoice} {
		for _, choice := range []int{0, 1, callbacktoken.MaxTarget, callbacktoken.MaxTarget + 1, -1} {
			button := telegramui.Button{Action: action, Label: "test model", Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: choice}}
			markup, err := presenter.PresentKeyboard(telegramui.GlobalSurfaceID, []string{testLogicalSessionID}, oneButton(button))
			invalid := choice < 0 || choice > callbacktoken.MaxTarget || (choice == 0 && (action == telegramui.ActionModelChoice || action == telegramui.ActionEffortChoice))
			if invalid {
				if err == nil {
					t.Fatalf("accepted %s choice %d", action, choice)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			out := markup.InlineKeyboard[0][0]
			fields, err := codec.Decode(out.CallbackData)
			if err != nil || fields.SessionID != testLogicalSessionID || fields.Target != choice || out.Text != button.Label || len(out.CallbackData) != 64 {
				t.Fatalf("selector = %#v fields=%#v err=%v", out, fields, err)
			}
		}
	}
}
