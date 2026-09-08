package telegramruntimecomposition

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestAutoApprovalSettingsCallbackRoundTrip(t *testing.T) {
	callback := telegrampipeline.AcceptedCallback{UpdateID: 1, SessionID: domain.SessionID(telegramui.GlobalSurfaceID), Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2}, Action: telegramui.ActionSettingsAutoApproveCommands}
	plan, err := telegrampipeline.PlanAcceptedCallback(callback)
	if err != nil || plan.Effect != telegrampipeline.EffectChangeSettings {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	semantic, err := semanticActionFromPlan(plan)
	if err != nil || semantic.Kind != telegramcontroller.SemanticSettingsAutoApproveCommands || semantic.SessionID != "" {
		t.Fatalf("semantic=%+v err=%v", semantic, err)
	}
	surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{Text: "CLI", Rows: [][]telegramcontroller.SemanticButton{{{Label: "Автоподтверждение Codex", Action: telegramcontroller.SemanticSettingsAutoApproveCommands}}}})
	if err != nil || surface.Keyboard.Rows[0][0].Action != telegramui.ActionSettingsAutoApproveCommands {
		t.Fatalf("surface=%+v err=%v", surface, err)
	}
	callback.Target.Choice = 1
	if _, err := telegrampipeline.PlanAcceptedCallback(callback); err == nil {
		t.Fatal("unexpected target accepted")
	}
}
