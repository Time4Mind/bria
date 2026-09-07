package telegrambridge_test

import (
	"testing"
	"time"

	"bria/internal/telegrambridge"
	"bria/internal/telegramui"
)

func TestPresenterPreservesArchiveLabelsAndSignsPageTargets(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	presenter := mustPresenter(t, mustCallbackCodec(t, func() time.Time { return now }), func() time.Time { return now }, time.Minute)
	markup, err := presenter.PresentKeyboard(telegramui.GlobalSurfaceID, []string{testSelectableSessionIDs[0]}, telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{
		{{Action: telegramui.ActionResume, Label: "7. named", Target: telegramui.ButtonTarget{SessionSlot: 1}}},
		{{Action: telegramui.ActionMenuArchive, Label: "◀", Target: telegramui.ButtonTarget{Choice: 1}}, {Action: telegramui.ActionMenuArchive, Label: "2/3", Target: telegramui.ButtonTarget{Choice: 2}}, {Action: telegramui.ActionMenuArchive, Label: "▶", Target: telegramui.ButtonTarget{Choice: 3}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if markup.InlineKeyboard[0][0].Text != "7. named" {
		t.Fatalf("resume label = %q", markup.InlineKeyboard[0][0].Text)
	}
	for index, want := range []int{1, 2, 3} {
		decoded, err := presenter.DecodeCallback(markup.InlineKeyboard[1][index].CallbackData)
		if err != nil || decoded != (telegrambridge.Callback{SessionID: telegramui.GlobalSurfaceID, Action: telegramui.ActionMenuArchive, Target: telegramui.ButtonTarget{Choice: want}}) {
			t.Fatalf("pager %d = %#v, %v", index, decoded, err)
		}
	}
}
