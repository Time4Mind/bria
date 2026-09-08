package telegramui_test

import (
	"testing"

	"bria/internal/telegramui"
)

func TestRecoveryKeyboardPreservesHistoryAndRecoveryActions(t *testing.T) {
	for _, expanded := range []bool{false, true} {
		keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{View: telegramui.PageView{Page: 1, Pages: 3}, Recovery: true, OptionsExpanded: expanded, SessionRowSizes: []int{2}, SessionLabels: []string{"recovering", "ready"}})
		if err != nil {
			t.Fatal(err)
		}
		counts := map[telegramui.Action]int{}
		for _, row := range keyboard.Rows {
			for _, button := range row {
				counts[button.Action]++
				if button.Action == telegramui.ActionResume && button.Label != "Восстановить" {
					t.Fatalf("recovery label=%q", button.Label)
				}
			}
		}
		for _, action := range []telegramui.Action{telegramui.ActionResume, telegramui.ActionClose, telegramui.ActionPagePrevious, telegramui.ActionPageLatest, telegramui.ActionPageNext, telegramui.ActionMenuBack} {
			if counts[action] != 1 {
				t.Errorf("recovery action %s count=%d", action, counts[action])
			}
		}
		for _, action := range []telegramui.Action{telegramui.ActionScreen, telegramui.ActionOptions, telegramui.ActionStop} {
			if counts[action] != 0 {
				t.Errorf("unsafe recovery control %s visible", action)
			}
		}
		if counts[telegramui.ActionSelectSession] != 2 || keyboard.Rows[0][0].Target.Page != 3 || keyboard.Rows[0][2].Target.Page != 2 {
			t.Fatalf("history/switching lost: %+v", keyboard)
		}
	}
}

func TestRecoveryKeyboardAllowsArchiveConfirmation(t *testing.T) {
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{View: telegramui.PageView{Page: 1, Pages: 1}, Recovery: true, CloseConfirmation: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(keyboard.Rows) != 1 || len(keyboard.Rows[0]) != 2 || keyboard.Rows[0][0].Action != telegramui.ActionClose || keyboard.Rows[0][0].Target.Choice != 1 || keyboard.Rows[0][0].Label != "Архивировать" || keyboard.Rows[0][1].Target.Choice != 2 {
		t.Fatalf("recovery confirmation=%+v", keyboard)
	}
}
