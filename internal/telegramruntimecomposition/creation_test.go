package telegramruntimecomposition

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestCreationDraftActionsMapAcrossSignedRuntimeBoundary(t *testing.T) {
	tests := []struct {
		action telegramui.Action
		effect telegrampipeline.CallbackEffect
		kind   telegramcontroller.SemanticActionKind
		target telegramui.ButtonTarget
	}{
		{telegramui.ActionCreateSelectCodex, telegrampipeline.EffectSelectCreateCodex, telegramcontroller.SemanticCreateSelectCodex, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateSelectClaude, telegrampipeline.EffectSelectCreateClaude, telegramcontroller.SemanticCreateSelectClaude, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateChoice, telegrampipeline.EffectCreateChoice, telegramcontroller.SemanticCreateChoice, telegramui.ButtonTarget{Choice: 2}},
		{telegramui.ActionCreatePrevious, telegrampipeline.EffectNavigateCreate, telegramcontroller.SemanticCreatePrevious, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateFirst, telegrampipeline.EffectNavigateCreate, telegramcontroller.SemanticCreateFirst, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateNext, telegrampipeline.EffectNavigateCreate, telegramcontroller.SemanticCreateNext, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateUp, telegrampipeline.EffectAdvanceCreate, telegramcontroller.SemanticCreateUp, telegramui.ButtonTarget{}},
		{telegramui.ActionCreatePick, telegrampipeline.EffectAdvanceCreate, telegramcontroller.SemanticCreatePick, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateDirectoryNew, telegrampipeline.EffectAdvanceCreate, telegramcontroller.SemanticCreateDirectoryNew, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateBack, telegrampipeline.EffectAdvanceCreate, telegramcontroller.SemanticCreateBack, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateFresh, telegrampipeline.EffectAdvanceCreate, telegramcontroller.SemanticCreateFresh, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateWorkdir, telegrampipeline.EffectEditCreateWorkdir, telegramcontroller.SemanticCreateWorkdir, telegramui.ButtonTarget{}},
		{telegramui.ActionCreateConfirm, telegrampipeline.EffectConfirmCreate, telegramcontroller.SemanticCreateConfirm, telegramui.ButtonTarget{}},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
				UpdateID:  1,
				SessionID: domain.SessionID(telegramui.GlobalSurfaceID),
				Carrier:   telegramstate.Carrier{ChatID: 1, MessageID: 2},
				Action:    test.action,
				Target:    test.target,
			})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Effect != test.effect {
				t.Fatalf("planned effect = %q, want %q", plan.Effect, test.effect)
			}
			semantic, err := semanticActionFromPlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			if semantic.Kind != test.kind || semantic.UpdateID != 1 || semantic.SessionID != "" || semantic.Choice != test.target.Choice {
				t.Fatalf("semantic action = %#v, want kind %q and claimed update", semantic, test.kind)
			}
			if got := callbackEffectForAction(test.action); got != test.effect {
				t.Fatalf("callback effect = %q, want %q", got, test.effect)
			}
			projected, err := telegramUIAction(test.kind)
			if err != nil || projected != test.action {
				t.Fatalf("projected action = (%q, %v), want %q", projected, err, test.action)
			}
		})
	}
}

func TestSettingsCategoryMapsAcrossSignedRuntimeBoundary(t *testing.T) {
	target := telegramui.ButtonTarget{Choice: 4}
	plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
		UpdateID: 1, SessionID: domain.SessionID(telegramui.GlobalSurfaceID),
		Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2},
		Action:  telegramui.ActionSettingsCategory, Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Effect != telegrampipeline.EffectOpenSettings {
		t.Fatalf("effect = %q", plan.Effect)
	}
	semantic, err := semanticActionFromPlan(plan)
	if err != nil || semantic.Kind != telegramcontroller.SemanticSettingsCategory || semantic.Choice != 4 {
		t.Fatalf("semantic category = (%#v, %v)", semantic, err)
	}
	projected, err := telegramUIAction(semantic.Kind)
	if err != nil || projected != telegramui.ActionSettingsCategory {
		t.Fatalf("projected category = (%q, %v)", projected, err)
	}
}

func TestNodeActionsMapAcrossSignedRuntimeBoundary(t *testing.T) {
	tests := []struct {
		action telegramui.Action
		kind   telegramcontroller.SemanticActionKind
		target telegramui.ButtonTarget
	}{
		{telegramui.ActionMenuNodes, telegramcontroller.SemanticMenuNodes, telegramui.ButtonTarget{}},
		{telegramui.ActionSelectNode, telegramcontroller.SemanticSelectNode, telegramui.ButtonTarget{Choice: 2}},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
				UpdateID: 1, SessionID: domain.SessionID(telegramui.GlobalSurfaceID),
				Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2},
				Action:  test.action, Target: test.target,
			})
			if err != nil {
				t.Fatal(err)
			}
			semantic, err := semanticActionFromPlan(plan)
			if err != nil || semantic.Kind != test.kind || semantic.Choice != test.target.Choice {
				t.Fatalf("semantic node action = (%#v, %v)", semantic, err)
			}
			projected, err := telegramUIAction(test.kind)
			if err != nil || projected != test.action {
				t.Fatalf("projected node action = (%q, %v)", projected, err)
			}
		})
	}
}

func TestCloseConfirmationMapsAcrossSignedRuntimeBoundary(t *testing.T) {
	for _, test := range []struct {
		target telegramui.ButtonTarget
	}{
		{}, {target: telegramui.ButtonTarget{Choice: 1}}, {target: telegramui.ButtonTarget{Choice: 2}},
	} {
		plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
			UpdateID: 1, SessionID: "11111111-1111-4111-9111-111111111111",
			Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2}, Action: telegramui.ActionClose, Target: test.target,
		})
		if err != nil || plan.Effect != telegrampipeline.EffectCloseSession {
			t.Fatalf("plan %#v = (%#v, %v)", test.target, plan, err)
		}
		semantic, err := semanticActionFromPlan(plan)
		if err != nil || semantic.Kind != telegramcontroller.SemanticClose || semantic.Choice != test.target.Choice {
			t.Fatalf("semantic %#v = (%#v, %v)", test.target, semantic, err)
		}
	}
}
