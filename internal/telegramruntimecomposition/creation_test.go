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
