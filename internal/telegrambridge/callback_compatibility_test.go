package telegrambridge_test

import (
	"reflect"
	"testing"
	"time"

	"bria/internal/telegrambridge"
	"bria/internal/telegramui"
)

func TestMixedCallbackPresentationPreservesTargetsAndManifestOrder(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{
			{Action: telegramui.ActionClose, Label: "Закрыть карточку", Target: telegramui.ButtonTarget{Choice: 2}},
			{Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{Page: 7, FollowLatest: true}, Indicator: &telegramui.PageIndicator{Current: 3, Total: 7}},
		},
		{
			{Action: telegramui.ActionResume, Label: "Архивная сессия", Target: telegramui.ButtonTarget{SessionSlot: 1}},
			{Action: telegramui.ActionNativeKey, Label: "Enter", Target: telegramui.ButtonTarget{SessionSlot: 2, Choice: 5}},
		},
		{{Action: telegramui.ActionMenuArchive, Label: "Следующая страница", Target: telegramui.ButtonTarget{Choice: 4}}},
	}}
	presentation, err := presenter.PresentKeyboardWithManifest(testLogicalSessionID, testSelectableSessionIDs[:2], keyboard)
	if err != nil {
		t.Fatal(err)
	}
	wantLabels := [][]string{{"Закрыть карточку", "3/7"}, {"Архивная сессия", "Enter"}, {"Следующая страница"}}
	if got := labels(presentation.Markup.InlineKeyboard); !reflect.DeepEqual(got, wantLabels) {
		t.Fatalf("labels = %#v, want %#v", got, wantLabels)
	}
	want := []telegrambridge.Callback{
		{SessionID: testLogicalSessionID, Action: telegramui.ActionClose, Target: telegramui.ButtonTarget{Choice: 2}},
		{SessionID: testLogicalSessionID, Action: telegramui.ActionPageLatest, Target: telegramui.ButtonTarget{FollowLatest: true}},
		{SessionID: testSelectableSessionIDs[0], Action: telegramui.ActionResume},
		{SessionID: testSelectableSessionIDs[1], Action: telegramui.ActionNativeKey, Target: telegramui.ButtonTarget{Choice: 5}},
		{SessionID: telegramui.GlobalSurfaceID, Action: telegramui.ActionMenuArchive, Target: telegramui.ButtonTarget{Choice: 4}},
	}
	if len(presentation.TokenIDs) != len(want) {
		t.Fatalf("manifest has %d tokens, want %d", len(presentation.TokenIDs), len(want))
	}
	index := 0
	for _, row := range presentation.Markup.InlineKeyboard {
		for _, button := range row {
			decoded, err := presenter.DecodeCallbackWithMetadata(button.CallbackData)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Callback != want[index] || decoded.TokenID != presentation.TokenIDs[index] || decoded.ExpiresAt != now.Add(time.Minute) {
				t.Fatalf("callback %d = %#v, want %#v with matching manifest position and expiry", index, decoded, want[index])
			}
			index++
		}
	}
}
