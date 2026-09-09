package telegramui_test

import (
	"reflect"
	"testing"

	"bria/internal/telegramui"
)

func TestProjectionCopiesDoNotShareMutableContainers(t *testing.T) {
	keyboard := telegramui.CardKeyboard{Rows: []telegramui.ButtonRow{{{Label: "original"}}}}
	projection := telegramui.CarrierProjection{
		Effect: telegramui.EffectEditSameCarrier, PreviousCardUnchanged: true,
		Card: telegramui.ProjectedCard{
			Pages:    []telegramui.ContentPage{{Content: "original", Anchors: []string{"anchor"}}},
			Keyboard: keyboard,
		},
		Notification: &telegramui.BackgroundCompletionNotification{Count: 1, Action: telegramui.ActionOptions},
	}
	copy := telegramui.CloneCarrierProjection(projection)
	if !reflect.DeepEqual(copy, projection) {
		t.Fatal("copy changed projection values")
	}
	copy.Card.Pages[0].Content = "changed"
	copy.Card.Pages[0].Anchors[0] = "changed"
	copy.Card.Keyboard.Rows[0][0].Label = "changed"
	copy.Notification.Count = 2
	if projection.Card.Pages[0].Content != "original" || projection.Card.Pages[0].Anchors[0] != "anchor" ||
		projection.Card.Keyboard.Rows[0][0].Label != "original" || projection.Notification.Count != 1 {
		t.Errorf("copy mutation leaked to projection: %+v", projection)
	}
	keyboardCopy := telegramui.CloneCardKeyboard(keyboard)
	keyboardCopy.Rows[0][0].Label = "surface"
	if keyboard.Rows[0][0].Label != "original" {
		t.Error("surface keyboard shares row storage")
	}
	if empty := telegramui.CloneCarrierProjection(telegramui.CarrierProjection{}); empty.Notification != nil || len(empty.Card.Pages) != 0 || len(empty.Card.Keyboard.Rows) != 0 {
		t.Fatalf("empty copy = %+v", empty)
	}
}
