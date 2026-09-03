package interactioncomposition_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/interactioncomposition"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
)

type callbackStub struct{ calls int }

type deleteStub struct{}

func (deleteStub) DeleteMessage(context.Context, telegram.DeleteMessageRequest) error { return nil }

func (stub *callbackStub) HandleCallback(context.Context, telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	stub.calls++
	return telegramflow.CallbackResult{OperationID: "handled"}, nil
}

func TestOpenProvidesBothControllerInteractionPorts(t *testing.T) {
	composition, err := interactioncomposition.Open(interactioncomposition.Options{
		StorePath:      filepath.Join(t.TempDir(), "interactions.json"),
		ConversationID: 42,
		OwnerActorID:   7,
		Telegram:       deleteStub{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var interaction telegramcontroller.InteractionHandler = composition.Flow()
	var text telegramcontroller.InteractionTextHandler = composition.Flow()
	if interaction == nil || text == nil {
		t.Fatal("composition did not expose controller interaction ports")
	}
}

func TestCallbackRouterDelegatesOnlyTypedInteractionPlans(t *testing.T) {
	normal := &callbackStub{}
	interaction := &callbackStub{}
	router, err := interactioncomposition.NewCallbackRouter(normal, interaction)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{OperationID: "normal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.HandleCallback(context.Background(), telegrampipeline.CallbackPlan{
		OperationID: "interaction", Interaction: &telegrampipeline.CallbackInteraction{RequestID: "request"},
	}); err != nil {
		t.Fatal(err)
	}
	if normal.calls != 1 || interaction.calls != 1 {
		t.Fatalf("callback calls normal/interaction = %d/%d", normal.calls, interaction.calls)
	}
}
