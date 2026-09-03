package telegramui_test

import (
	"reflect"
	"testing"

	"bria/internal/telegramui"
)

func TestProjectCardKeyboardKeepsCanonicalSemanticRowOrder(t *testing.T) {
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{
		View:            telegramui.PageView{Page: 1, Pages: 3},
		Working:         true,
		OptionsExpanded: true,
		SessionRowSizes: []int{2, 1},
	})
	if err != nil {
		t.Fatalf("ProjectCardKeyboard() error = %v", err)
	}

	want := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{
			{Action: telegramui.ActionPagePrevious, Target: telegramui.ButtonTarget{Page: 3}},
			{
				Action:    telegramui.ActionPageLatest,
				Target:    telegramui.ButtonTarget{Page: 3, FollowLatest: true},
				Indicator: &telegramui.PageIndicator{Current: 1, Total: 3},
			},
			{Action: telegramui.ActionPageNext, Target: telegramui.ButtonTarget{Page: 2}},
		},
		{
			{Action: telegramui.ActionStop},
			{Action: telegramui.ActionOptions},
		},
		{{Action: telegramui.ActionScreen}},
		{
			{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 1}},
			{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 2}},
		},
		{{Action: telegramui.ActionSelectSession, Target: telegramui.ButtonTarget{SessionSlot: 3}}},
	}}

	if !reflect.DeepEqual(keyboard, want) {
		t.Fatalf("ProjectCardKeyboard() = %#v, want %#v", keyboard, want)
	}
}

func TestProjectCardKeyboardUsesResumeForArchivedSession(t *testing.T) {
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{
		View:     telegramui.PageView{Page: 1, Pages: 1},
		Archived: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if keyboard.Rows[1][0].Action != telegramui.ActionResume {
		t.Fatalf("archived lifecycle action = %q, want resume", keyboard.Rows[1][0].Action)
	}
}

func TestProjectCardKeyboardUsesCloseAndOmitsCollapsedOptionsRow(t *testing.T) {
	keyboard, err := telegramui.ProjectCardKeyboard(telegramui.CardKeyboardInput{
		View: telegramui.PageView{Page: 1, Pages: 1},
	})
	if err != nil {
		t.Fatalf("ProjectCardKeyboard() error = %v", err)
	}

	if got, want := len(keyboard.Rows), 2; got != want {
		t.Fatalf("row count = %d, want %d", got, want)
	}
	if got, want := keyboard.Rows[1], (telegramui.ButtonRow{
		{Action: telegramui.ActionClose},
		{Action: telegramui.ActionOptions},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("control row = %#v, want %#v", got, want)
	}
}
