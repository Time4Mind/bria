package telegramruntimecomposition

import (
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

func TestArchivePageMapsAcrossSignedRuntimeBoundary(t *testing.T) {
	plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{
		UpdateID: 1, SessionID: domain.SessionID(telegramui.GlobalSurfaceID),
		Carrier: telegramstate.Carrier{ChatID: 1, MessageID: 2},
		Action:  telegramui.ActionMenuArchive, Target: telegramui.ButtonTarget{Choice: 3},
	})
	if err != nil || plan.Effect != telegrampipeline.EffectOpenArchive {
		t.Fatalf("archive plan = (%#v, %v)", plan, err)
	}
	semantic, err := semanticActionFromPlan(plan)
	if err != nil || semantic.Kind != telegramcontroller.SemanticMenuArchive || semantic.Choice != 3 || semantic.SessionID != "" {
		t.Fatalf("archive semantic action = (%#v, %v)", semantic, err)
	}

	surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{
		Text: "archive", Rows: [][]telegramcontroller.SemanticButton{
			{{Label: "7. named", Action: telegramcontroller.SemanticResume, SessionID: "11111111-1111-4111-9111-111111111111"}},
			{{Label: "◀", Action: telegramcontroller.SemanticMenuArchive, Choice: 2}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resume := surface.Keyboard.Rows[0][0]
	pager := surface.Keyboard.Rows[1][0]
	if resume.Label != "7. named" || resume.Target.SessionSlot != 1 || pager.Label != "◀" || pager.Target.Choice != 2 {
		t.Fatalf("archive projection = %#v", surface)
	}
}
