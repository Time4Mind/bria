package telegramui_test

import (
	"fmt"
	"reflect"
	"testing"

	"bria/internal/telegramui"
)

func TestCompletionSelectsCurrentTypedFinalStartInBothReadingModes(t *testing.T) {
	for _, long := range []bool{false, true} {
		for _, follow := range []bool{false, true} {
			t.Run(fmt.Sprintf("long=%t/follow=%t", long, follow), func(t *testing.T) {
				pages := []telegramui.ContentPage{
					{Content: "old final", Anchors: []string{"opaque-a"}, FinalStart: true},
					{Content: "prompt and working commentary", Anchors: []string{"opaque-b"}},
					{Content: "current final start", Anchors: []string{"opaque-c"}, FinalStart: true},
				}
				if long {
					pages = append(pages, telegramui.ContentPage{Content: "middle", Anchors: []string{"opaque-d"}}, telegramui.ContentPage{Content: "tail", Anchors: []string{"opaque-e"}})
				}
				view := telegramui.PageView{Page: 1, Pages: len(pages), Anchor: "opaque-a"}
				if follow {
					view.Page, view.Anchor, view.FollowLatest = len(pages), pages[len(pages)-1].Anchors[0], true
				}
				input := telegramui.CardProjectionInput{Pages: pages, View: view}
				before := deepCopyProjectionInput(input)
				for _, active := range []bool{true, false} {
					got, err := telegramui.ProjectCompletion(input, active)
					if err != nil {
						t.Fatal(err)
					}
					want := telegramui.PageView{Page: 3, Pages: len(pages), Anchor: "opaque-c", FollowLatest: false}
					if got.Card.View != want {
						t.Fatalf("active=%t completion view=%+v want=%+v", active, got.Card.View, want)
					}
					if !got.PreviousCardUnchanged || !reflect.DeepEqual(input, before) || !reflect.DeepEqual(got.Card.Pages, pages) {
						t.Fatal("completion changed previous card or history")
					}
					if active && (got.Effect != telegramui.EffectSendOneNewCard || got.Notification != nil) {
						t.Fatal("active final did not request one new card")
					}
					if !active && (got.Effect != telegramui.EffectSendOneBackgroundCompletion || got.Notification == nil || got.Notification.ContainsFinal) {
						t.Fatal("background final replaced chosen view")
					}
					indicator := got.Card.Keyboard.Rows[0][1].Indicator
					if indicator == nil || indicator.Current != 3 || indicator.Total != len(pages) {
						t.Fatal("new card keyboard did not select final start")
					}
				}
			})
		}
	}
}

func TestCompletionWithoutTypedMarkerKeepsLegacyFallback(t *testing.T) {
	// Neither prose nor an anchor spelled "final" is evidence of a boundary.
	input := telegramui.CardProjectionInput{
		Pages: []telegramui.ContentPage{{Content: "final start", Anchors: []string{"final"}}, {Content: "tail", Anchors: []string{"tail"}}},
		View:  telegramui.PageView{Page: 1, Pages: 2, Anchor: "final"},
	}
	got, err := telegramui.ProjectActiveFinal(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Card.View.Page != 2 || !got.Card.View.FollowLatest {
		t.Fatalf("legacy fallback=%+v", got.Card.View)
	}
}
