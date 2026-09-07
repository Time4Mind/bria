package telegramflow_test

import (
	"testing"
	"time"

	"bria/internal/telegramflow"
	"bria/internal/telegramui"
)

func TestScreenMarkerOnlyForOrdinaryCardRefresh(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		archived, close, delete bool
	}{
		{name: "active"}, {name: "archive", archived: true}, {name: "close", close: true}, {name: "delete", delete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := telegramui.PageView{Page: 1, Pages: 1}
			prepared, err := telegramflow.PrepareCardRefresh("screen-test", flowSessionID, 42, 501,
				telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: "content"}}, View: view,
					Keyboard: telegramui.CardKeyboardInput{View: view, Archived: tc.archived, CloseConfirmation: tc.close, DeleteConfirmation: tc.delete}}, "header", false, nil, newPresenter(t, time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			if (prepared.Status.ScreenSessionID == string(flowSessionID)) != (tc.name == "active") {
				t.Fatalf("wrong Screen marker: %q", prepared.Status.ScreenSessionID)
			}
			if prepared.Status.SourceMessageID != 501 || !prepared.Edit {
				t.Fatal("Screen changed carrier operation")
			}
		})
	}
}
