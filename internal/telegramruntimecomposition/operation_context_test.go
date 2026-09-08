package telegramruntimecomposition

import (
	"context"
	"testing"

	"bria/internal/controllertelemetry"
	"bria/internal/coordinator"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

type operationController struct{ operation string }

func (c *operationController) HandleSemanticMessage(context.Context, coordinator.Update) (telegramcontroller.SemanticActionResult, error) {
	return telegramcontroller.SemanticActionResult{}, nil
}

func (c *operationController) HandleSemanticAction(ctx context.Context, _ telegramcontroller.SemanticAction) (telegramcontroller.SemanticActionResult, error) {
	c.operation = controllertelemetry.Operation(ctx)
	return telegramcontroller.SemanticActionResult{Surface: &telegramcontroller.SemanticSurface{Text: "sessions", Rows: [][]telegramcontroller.SemanticButton{{{Label: "Menu", Action: telegramcontroller.SemanticMenuBack}}}}}, nil
}

func TestCallbackCarriesClaimedOperationIntoControllerDiagnostics(t *testing.T) {
	c := &operationController{}
	_, err := (ControllerFlowAdapter{Controller: c}).HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
		OperationID: "claimed-close", UpdateID: 1, Action: telegramui.ActionClose, Effect: telegrampipeline.EffectCloseSession,
		SessionID: "11111111-1111-4111-9111-111111111111", Target: telegramui.ButtonTarget{Choice: 1},
	})
	if err != nil || c.operation != "claimed-close" {
		t.Fatalf("operation=%q err=%v", c.operation, err)
	}
}
