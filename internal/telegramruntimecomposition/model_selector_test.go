package telegramruntimecomposition

import (
	"bria/internal/callbacktoken"
	"bria/internal/domain"
	"bria/internal/telegrambridge"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
	"bytes"
	"testing"
	"time"
)

func TestModelSelectorAndBackSignedRoundTrip(t *testing.T) {
	const session = domain.SessionID("11111111-1111-4111-9111-111111111111")
	now := time.Unix(1_800_000_000, 0).UTC()
	codec, err := callbacktoken.New(bytes.Repeat([]byte{7}, 32), nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := telegrambridge.NewPresenter(codec, func() time.Time { return now }, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []telegramcontroller.SemanticActionKind{telegramcontroller.SemanticNativeKey, telegramcontroller.SemanticModelMenu, telegramcontroller.SemanticModelChoice, telegramcontroller.SemanticEffortMenu, telegramcontroller.SemanticEffortChoice, telegramcontroller.SemanticSelect} {
		choice, label := 2, "model"
		if kind == telegramcontroller.SemanticSelect {
			choice, label = 0, "← К сессии"
		}
		surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{Text: "Models", Rows: [][]telegramcontroller.SemanticButton{{{Label: label, Action: kind, SessionID: session, Choice: choice}}}})
		if err != nil {
			t.Fatal(err)
		}
		markup, err := presenter.PresentKeyboard(telegramui.GlobalSurfaceID, []string{string(surface.SelectableSessionIDs[0])}, surface.Keyboard)
		if err != nil {
			t.Fatal(err)
		}
		button := markup.InlineKeyboard[0][0]
		if button.Text != label {
			t.Fatalf("label=%q want=%q", button.Text, label)
		}
		decoded, err := presenter.DecodeCallback(button.CallbackData)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{UpdateID: 1, SessionID: domain.SessionID(decoded.SessionID), Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2}, Action: decoded.Action, Target: decoded.Target})
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := semanticActionFromPlan(plan)
		if err != nil || semantic.Kind != kind || semantic.SessionID != session || semantic.Choice != choice {
			t.Fatalf("semantic=%#v err=%v", semantic, err)
		}
	}
}

func TestModelSelectorSurfaceAndCallbackKeepExactChoiceAndSession(t *testing.T) {
	const session = domain.SessionID("11111111-1111-4111-9111-111111111111")
	for _, kind := range []telegramcontroller.SemanticActionKind{telegramcontroller.SemanticNativeKey, telegramcontroller.SemanticModelMenu, telegramcontroller.SemanticModelChoice, telegramcontroller.SemanticEffortMenu, telegramcontroller.SemanticEffortChoice} {
		surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{Text: "Models", Rows: [][]telegramcontroller.SemanticButton{{{Label: "chosen", Action: kind, SessionID: session, Choice: 2}}}})
		if err != nil {
			t.Fatal(err)
		}
		button := surface.Keyboard.Rows[0][0]
		if button.Label != "chosen" || button.Target.Choice != 2 || button.Target.SessionSlot != 1 || len(surface.SelectableSessionIDs) != 1 || surface.SelectableSessionIDs[0] != session {
			t.Fatalf("surface=%#v", surface)
		}
		accepted := telegrampipeline.AcceptedCallback{UpdateID: 1, SessionID: session, Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2}, Action: button.Action, Target: telegramui.ButtonTarget{Choice: 2}}
		plan, err := telegrampipeline.PlanAcceptedCallback(accepted)
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := semanticActionFromPlan(plan)
		if err != nil || semantic.Kind != kind || semantic.SessionID != session || semantic.Choice != 2 {
			t.Fatalf("semantic=%#v err=%v", semantic, err)
		}
		accepted.SessionID = domain.SessionID(telegramui.GlobalSurfaceID)
		if _, err := telegrampipeline.PlanAcceptedCallback(accepted); err == nil {
			t.Fatal("accepted global selector")
		}
		accepted.SessionID = session
		accepted.Target.Choice = 65536
		if _, err := telegrampipeline.PlanAcceptedCallback(accepted); err == nil {
			t.Fatal("accepted overflowing selector")
		}
		if _, err := projectSemanticSurface(telegramcontroller.SemanticSurface{Text: "Models", Rows: [][]telegramcontroller.SemanticButton{{{Label: "chosen", Action: kind, Choice: 2}}}}); err == nil {
			t.Fatal("accepted unbound selector")
		}
	}
}
