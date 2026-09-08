package telegramcallbackview_test

import (
	"testing"

	"bria/internal/callbacktoken"
	"bria/internal/telegramcallbackview"
	"bria/internal/telegramui"
)

func TestButtonPresentationAndAuthenticatedFieldProjection(t *testing.T) {
	tests := []struct {
		name    string
		button  telegramui.Button
		label   string
		action  callbacktoken.Action
		target  int
		decoded telegramui.ButtonTarget
	}{
		{
			name: "latest keeps follow intent instead of displayed page",
			button: telegramui.Button{Action: telegramui.ActionPageLatest,
				Target: telegramui.ButtonTarget{Page: 7, FollowLatest: true}, Indicator: &telegramui.PageIndicator{Current: 3, Total: 7}},
			label: "3/7", action: callbacktoken.ActionLatestPage, decoded: telegramui.ButtonTarget{FollowLatest: true},
		},
		{
			name:   "session slot is resolved by caller",
			button: telegramui.Button{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 2}},
			label:  "Сессия 2", action: callbacktoken.ActionSelectSession,
		},
		{
			name:   "custom native key label and bounded choice",
			button: telegramui.Button{Action: telegramui.ActionNativeKey, Label: "Enter", Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: 5}},
			label:  "Enter", action: callbacktoken.ActionNativeKey, target: 5, decoded: telegramui.ButtonTarget{Choice: 5},
		},
		{
			name:   "interaction choice keeps separate namespace",
			button: telegramui.Button{Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 2}},
			label:  "Вариант 2", action: callbacktoken.ActionInteractionChoice, target: 2, decoded: telegramui.ButtonTarget{InteractionChoice: 2},
		},
		{
			name:   "recovery warning remains visible",
			button: telegramui.Button{Action: telegramui.ActionCallbackSendRetryPossibleDuplicate},
			label:  "Повторить отправку (риск дубля)", action: callbacktoken.ActionCallbackSendRetryPossibleDuplicate,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			label, action, target, err := telegramcallbackview.PresentButton(test.button)
			if err != nil || label != test.label || action != test.action || target != test.target {
				t.Fatalf("presentation = %q, %v, %d, %v", label, action, target, err)
			}
			decodedAction, decodedTarget, err := telegramcallbackview.DecodeFields(callbacktoken.Fields{Action: test.action, Target: test.target})
			if err != nil || decodedAction != test.button.Action || decodedTarget != test.decoded {
				t.Fatalf("projection = %q, %#v, %v", decodedAction, decodedTarget, err)
			}
		})
	}
}

func TestPresentationRejectsAmbiguousTargetsAndUnsupportedActions(t *testing.T) {
	buttons := []telegramui.Button{
		{Action: "future"},
		{Action: telegramui.ActionMenuBack, Target: telegramui.ButtonTarget{Choice: 1}},
		{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 1, SessionSlot: 1}},
		{Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{Page: 2, FollowLatest: true}, Indicator: &telegramui.PageIndicator{Current: 1, Total: 3}},
		{Action: telegramui.ActionScreen, Indicator: &telegramui.PageIndicator{Current: 1, Total: 1}},
		{Action: telegramui.ActionNativeKey, Label: "invalid", Target: telegramui.ButtonTarget{SessionSlot: 1, Choice: 9}},
		{Action: telegramui.ActionModelChoice, Label: "missing choice", Target: telegramui.ButtonTarget{SessionSlot: 1}},
		{Action: telegramui.ActionInteractionChoice, Target: telegramui.ButtonTarget{InteractionChoice: 1, Choice: 1}},
	}
	for _, button := range buttons {
		if label, action, target, err := telegramcallbackview.PresentButton(button); err == nil || label != "" || action != 0 || target != 0 {
			t.Fatalf("invalid button %#v returned %q, %v, %d, %v", button, label, action, target, err)
		}
	}
	if action, target, err := telegramcallbackview.DecodeFields(callbacktoken.Fields{}); err == nil || action != "" || target != (telegramui.ButtonTarget{}) {
		t.Fatalf("unsupported action returned %q, %#v, %v", action, target, err)
	}
}
