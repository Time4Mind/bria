package telegramui_test

import (
	"reflect"
	"testing"

	"bria/internal/telegramui"
)

func TestProjectPageNavigationEditsSameCarrier(t *testing.T) {
	pages := []telegramui.ContentPage{
		{Content: "old", Anchors: []string{"old"}},
		{Content: "latest", Anchors: []string{"latest"}},
	}
	projection, err := telegramui.ProjectPageNavigation(telegramui.CardProjectionInput{
		Pages: pages,
		View:  telegramui.PageView{Page: 2, Pages: 2, Anchor: "latest", FollowLatest: true},
		Keyboard: telegramui.CardKeyboardInput{
			Working: true,
		},
	}, telegramui.ActionPagePrevious)
	if err != nil {
		t.Fatalf("ProjectPageNavigation() error = %v", err)
	}

	if projection.Effect != telegramui.EffectEditSameCarrier {
		t.Fatalf("effect = %q, want %q", projection.Effect, telegramui.EffectEditSameCarrier)
	}
	if projection.PreviousCardUnchanged {
		t.Fatal("ordinary page edit unexpectedly requested a retained previous carrier")
	}
	if want := (telegramui.PageView{Page: 1, Pages: 2, Anchor: "old"}); projection.Card.View != want {
		t.Fatalf("view = %#v, want %#v", projection.Card.View, want)
	}
	if indicator := projection.Card.Keyboard.Rows[0][1].Indicator; indicator == nil || indicator.Current != 1 || indicator.Total != 2 {
		t.Fatalf("page indicator = %#v, want 1/2", indicator)
	}
}

func TestProjectActiveFinalSendsOneNewCardAndKeepsPreviousAndPagination(t *testing.T) {
	input := telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{
			{Content: "response start", Anchors: []string{"response"}},
			{Content: "response end", Anchors: []string{"response\x001"}},
		},
		View: telegramui.PageView{Page: 1, Pages: 2, Anchor: "response"},
		Keyboard: telegramui.CardKeyboardInput{
			SessionRowSizes: []int{1},
		},
	}
	before := deepCopyProjectionInput(input)

	projection, err := telegramui.ProjectActiveFinal(input)
	if err != nil {
		t.Fatalf("ProjectActiveFinal() error = %v", err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("ProjectActiveFinal() mutated input: before=%#v after=%#v", before, input)
	}
	if projection.Effect != telegramui.EffectSendOneNewCard || !projection.PreviousCardUnchanged {
		t.Fatalf("final effect = %#v, want one new card with previous unchanged", projection)
	}
	if projection.Card.View.Page != 2 || !projection.Card.View.FollowLatest {
		t.Fatalf("new final card view = %#v, want visible latest page", projection.Card.View)
	}
	if got, want := projection.Card.Pages, input.Pages; !reflect.DeepEqual(got, want) {
		t.Fatalf("final pages = %#v, want %#v", got, want)
	}
	wantActions := []telegramui.Action{
		telegramui.ActionPagePrevious,
		telegramui.ActionPageLatest,
		telegramui.ActionPageNext,
	}
	for index, want := range wantActions {
		if got := projection.Card.Keyboard.Rows[0][index].Action; got != want {
			t.Fatalf("pagination action %d = %q, want %q", index, got, want)
		}
	}
}

func TestProjectActiveFinalRejectsDuplicatePageAnchors(t *testing.T) {
	_, err := telegramui.ProjectActiveFinal(telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{
			{Content: "first", Anchors: []string{"response"}},
			{Content: "second", Anchors: []string{"response"}},
		},
		View: telegramui.PageView{Page: 2, Pages: 2, Anchor: "response"},
	})
	if err == nil {
		t.Fatal("ProjectActiveFinal() error = nil, want ambiguous anchor rejection")
	}
}

func TestProjectCompletionSeparatesActiveCardFromOneBackgroundNotification(t *testing.T) {
	input := telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{
			{Content: "user", Anchors: []string{"user"}},
			{Content: "SECRET FINAL", Anchors: []string{"final"}},
		},
		View: telegramui.PageView{Page: 1, Pages: 2, Anchor: "user"},
	}

	active, err := telegramui.ProjectCompletion(input, true)
	if err != nil {
		t.Fatal(err)
	}
	if active.Effect != telegramui.EffectSendOneNewCard || active.Notification != nil || !active.PreviousCardUnchanged {
		t.Fatalf("active completion = %#v", active)
	}

	background, err := telegramui.ProjectCompletion(input, false)
	if err != nil {
		t.Fatal(err)
	}
	if background.Effect != telegramui.EffectSendOneBackgroundCompletion || !background.PreviousCardUnchanged {
		t.Fatalf("background completion = %#v", background)
	}
	if background.Notification == nil || background.Notification.Count != 1 || background.Notification.Action != telegramui.ActionSelectSession {
		t.Fatalf("background notification = %#v, want exactly one session-select notification", background.Notification)
	}
	if background.Notification.ContainsFinal {
		t.Fatal("background notification exposes final content")
	}
	if got := background.Card.Pages[1].Content; got != "SECRET FINAL" {
		t.Fatalf("persistable background card lost final: %q", got)
	}
}

func deepCopyProjectionInput(input telegramui.CardProjectionInput) telegramui.CardProjectionInput {
	copyInput := input
	copyInput.Pages = make([]telegramui.ContentPage, len(input.Pages))
	for index, page := range input.Pages {
		copyInput.Pages[index] = page
		copyInput.Pages[index].Anchors = append([]string(nil), page.Anchors...)
	}
	copyInput.Keyboard.SessionRowSizes = append([]int(nil), input.Keyboard.SessionRowSizes...)
	return copyInput
}
