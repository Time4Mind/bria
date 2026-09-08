package telegramcallbackview_test

import (
	"testing"

	"bria/internal/callbacktoken"
	"bria/internal/telegramcallbackview"
	"bria/internal/telegramui"
)

func TestTechnicalOutputLinesRejectsUnexpectedTarget(t *testing.T) {
	button := telegramui.Button{Action: telegramui.ActionSettingsTechnicalOutputLines}
	label, action, target, err := telegramcallbackview.PresentButton(button)
	if err != nil || label != "Строки технического вывода" || action != callbacktoken.ActionSettingsTechnicalOutputLines || target != 0 {
		t.Fatalf("button=%s %d %d %v", label, action, target, err)
	}
	for _, invalid := range []telegramui.ButtonTarget{{Choice: 5}, {Page: 1}, {FollowLatest: true}, {SessionSlot: 1}, {InteractionChoice: 1}} {
		button.Target = invalid
		if _, _, _, err := telegramcallbackview.PresentButton(button); err == nil {
			t.Fatalf("accepted invalid cycle target %+v", invalid)
		}
	}
}
