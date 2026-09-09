package telegramui_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"bria/internal/telegramui"
)

func TestCompletionTargetsExactFinalInsteadOfNewerAnswer(t *testing.T) {
	pages := []telegramui.ContentPage{
		{Content: "A start", Anchors: []string{"one"}, FinalStart: true, FinalOperationID: "A:final"},
		{Content: "A continuation", Anchors: []string{"two"}, FinalOperationID: "A:final"},
		{Content: "B start", Anchors: []string{"three"}, FinalStart: true, FinalOperationID: "B:final"},
		{Content: "B tail", Anchors: []string{"four"}, FinalOperationID: "B:final"},
	}
	for _, follow := range []bool{false, true} {
		for _, active := range []bool{false, true} {
			for target, wantPage := range map[string]int{"A:final": 1, "B:final": 3} {
				input := telegramui.CardProjectionInput{Pages: pages, View: telegramui.PageView{Page: 2, Pages: 4, Anchor: "two", FollowLatest: follow}, TargetFinalOperationID: target}
				before := deepCopyProjectionInput(input)
				got, err := telegramui.ProjectCompletion(input, active)
				if err != nil {
					t.Fatal(err)
				}
				if got.Card.View.Page != wantPage || got.Card.View.Anchor != pages[wantPage-1].Anchors[0] || got.Card.View.FollowLatest {
					t.Fatalf("target=%q active=%t follow=%t view=%+v want page=%d pinned", target, active, follow, got.Card.View, wantPage)
				}
				if !got.PreviousCardUnchanged || !reflect.DeepEqual(input, before) || !reflect.DeepEqual(got.Card.Pages, pages) {
					t.Fatal("exact selection changed previous card or history")
				}
			}
		}
	}
}

func TestCompletionRejectsMissingOrAmbiguousExactFinalStart(t *testing.T) {
	for name, pages := range map[string][]telegramui.ContentPage{
		"other_final":           {{Content: "B", Anchors: []string{"one"}, FinalStart: true, FinalOperationID: "B:final"}},
		"retained_continuation": {{Content: "A tail", Anchors: []string{"one"}, FinalOperationID: "A:final"}},
		"unbound_legacy":        {{Content: "A:final", Anchors: []string{"A:final"}, FinalStart: true}},
		"duplicate_start":       {{Content: "first", Anchors: []string{"one"}, FinalStart: true, FinalOperationID: "A:final"}, {Content: "second", Anchors: []string{"two"}, FinalStart: true, FinalOperationID: "A:final"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, active := range []bool{true, false} {
				got, err := telegramui.ProjectCompletion(telegramui.CardProjectionInput{Pages: pages, View: telegramui.PageView{Page: 1, Pages: len(pages)}, TargetFinalOperationID: "A:final"}, active)
				if err == nil || got.Effect != "" {
					t.Fatalf("missing/ambiguous exact start produced carrier: %+v err=%v", got, err)
				}
				if strings.Contains(err.Error(), "A:final") {
					t.Fatal("error exposes exact operation identity")
				}
			}
		})
	}
}

func TestFinalIdentityFieldsOmitEmptyAndRoundTripExactTarget(t *testing.T) {
	input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "legacy", Anchors: []string{"one"}}}, View: telegramui.PageView{Page: 1, Pages: 1}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "FinalOperationID") {
		t.Fatalf("empty metadata changed old format: %s", data)
	}
	input.TargetFinalOperationID, input.Pages[0].FinalOperationID, input.Pages[0].FinalStart = "A:final", "A:final", true
	data, err = json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var restored telegramui.CardProjectionInput
	if err := json.Unmarshal(data, &restored); err != nil || !reflect.DeepEqual(restored, input) {
		t.Fatalf("exact metadata round trip: %v", err)
	}
}
