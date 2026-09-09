package telegramcallbackview_test

import (
	"testing"

	"bria/internal/callbacktoken"
	"bria/internal/telegramcallbackview"
	"bria/internal/telegramui"
)

func TestTechnicalOutputLinesRejectsUnexpectedTarget(t *testing.T) {
	testGlobalToggle(t, telegramui.ActionSettingsTechnicalOutputLines, "Строки технического вывода", callbacktoken.ActionSettingsTechnicalOutputLines)
}

func TestHiddenDirectoriesRejectsUnexpectedTarget(t *testing.T) {
	testGlobalToggle(t, telegramui.ActionSettingsHiddenDirectories, "Скрытые каталоги", callbacktoken.ActionSettingsHiddenDirectories)
}

func testGlobalToggle(t *testing.T, uiAction telegramui.Action, wantLabel string, wireAction callbacktoken.Action) {
	t.Helper()
	button := telegramui.Button{Action: uiAction}
	label, action, target, err := telegramcallbackview.PresentButton(button)
	if err != nil || label != wantLabel || action != wireAction || target != 0 {
		t.Fatalf("button=%s %d %d %v", label, action, target, err)
	}
	decoded, decodedTarget, err := telegramcallbackview.DecodeFields(callbacktoken.Fields{Action: wireAction})
	if err != nil || decoded != uiAction || decodedTarget != (telegramui.ButtonTarget{}) {
		t.Fatalf("decoded=%s %+v %v", decoded, decodedTarget, err)
	}
	for _, invalid := range []telegramui.ButtonTarget{{Choice: 5}, {Page: 1}, {FollowLatest: true}, {SessionSlot: 1}, {InteractionChoice: 1}} {
		button.Target = invalid
		if _, _, _, err := telegramcallbackview.PresentButton(button); err == nil {
			t.Fatalf("accepted invalid cycle target %+v", invalid)
		}
	}
}
