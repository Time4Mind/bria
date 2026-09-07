package telegramruntimecomposition

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestStandbyMapsThroughSettingsEffectAndBackToRichSurface(t *testing.T) {
	callback := telegrampipeline.AcceptedCallback{UpdateID: 1, SessionID: domain.SessionID(telegramui.GlobalSurfaceID), Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2}, Action: telegramui.ActionSettingsStandby}
	plan, err := telegrampipeline.PlanAcceptedCallback(callback)
	if err != nil || plan.Effect != telegrampipeline.EffectChangeSettings {
		t.Fatalf("plan=%#v error=%v", plan, err)
	}
	semantic, err := semanticActionFromPlan(plan)
	if err != nil || semantic.Kind != telegramcontroller.SemanticSettingsStandby || semantic.SessionID != "" {
		t.Fatalf("semantic=%#v error=%v", semantic, err)
	}
	surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{Text: "| Настройка | Значение |", RichMarkdown: true, Rows: [][]telegramcontroller.SemanticButton{{{Label: "Ожидающая сессия", Action: telegramcontroller.SemanticSettingsStandby}}}})
	if err != nil || !surface.RichMarkdown || surface.Keyboard.Rows[0][0].Action != telegramui.ActionSettingsStandby {
		t.Fatalf("surface=%#v error=%v", surface, err)
	}
	callback.Target.Choice = 1
	if _, err := telegrampipeline.PlanAcceptedCallback(callback); err == nil {
		t.Fatal("unexpected standby target accepted")
	}
}
