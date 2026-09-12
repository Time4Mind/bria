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
	if err != nil || surface.Keyboard.Rows[0][0].Action != telegramui.ActionSettingsAutoApproveCommands || surface.Keyboard.Rows[0][0].Label != "Автоподтверждение Codex" {
		t.Fatalf("surface=%+v err=%v", surface, err)
	}
	callback.Target.Choice = 1
	if _, err := telegrampipeline.PlanAcceptedCallback(callback); err == nil {
		t.Fatal("unexpected target accepted")
	}
}

func TestSettingsButtonsKeepLabelsAcrossRuntimeProjection(t *testing.T) {
	tests := []struct {
		label  string
		action telegramcontroller.SemanticActionKind
		want   telegramui.Action
	}{
		{"Автоимя", telegramcontroller.SemanticSettingsSessionNaming, telegramui.ActionSettingsSessionNaming},
		{"CLI по умолчанию", telegramcontroller.SemanticSettingsDefaultProvider, telegramui.ActionSettingsDefaultProvider},
		{"Переименовать ноду", telegramcontroller.SemanticSettingsRenameNode, telegramui.Action(telegramcontroller.SemanticSettingsRenameNode)},
		{"Авторизовать Codex", telegramcontroller.SemanticAuthorizeCodex, telegramui.ActionAuthorizeCodex},
		{"Авторизовать Claude", telegramcontroller.SemanticAuthorizeClaude, telegramui.ActionAuthorizeClaude},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{
				Text: "Настройки",
				Rows: [][]telegramcontroller.SemanticButton{{{
					Label: test.label, Action: test.action,
				}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			button := surface.Keyboard.Rows[0][0]
			if button.Action != test.want || button.Label != test.label {
				t.Fatalf("button = %+v, want action=%q label=%q", button, test.want, test.label)
			}
		})
	}
}
