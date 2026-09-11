package telegramflow_test

import (
	"testing"
	"time"

	"bria/internal/telegramflow"
	"bria/internal/telegramui"
)

func TestCardRenderModeStaysRichAcrossTableAndNonTablePages(t *testing.T) {
	for _, body := range []string{"| A | B |\n|---|---|\n| one | two |", "plain next page"} {
		view := telegramui.PageView{Page: 1, Pages: 1}
		input := telegramui.CardProjectionInput{Pages: []telegramui.ContentPage{{Content: body, Anchors: []string{"answer"}}}, View: view, Keyboard: telegramui.CardKeyboardInput{View: view}}
		presenter := newPresenter(t, time.Now())
		final, err := telegramflow.PrepareCompletion("rich-final", flowSessionID, 42, true, "", input, false, nil, presenter)
		if err != nil {
			t.Fatal(err)
		}
		if !final.Status.RichMarkdown || final.Edit || final.Status.ScreenSessionID != "" {
			t.Errorf("final must be Rich without forcing Screen: %+v", final.Status)
		}
		input.Keyboard.CloseConfirmation = true
		refresh, err := telegramflow.PrepareCardRefresh("rich-close", flowSessionID, 42, 55, input, "header", false, nil, presenter)
		if err != nil {
			t.Fatal(err)
		}
		if !refresh.Status.RichMarkdown || !refresh.Edit || refresh.Status.SourceMessageID != 55 || refresh.Status.ScreenSessionID != "" {
			t.Errorf("text-only card must keep Rich carrier: %+v", refresh.Status)
		}
	}
}
