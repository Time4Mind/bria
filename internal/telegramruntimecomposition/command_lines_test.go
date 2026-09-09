package telegramruntimecomposition_test

import (
	"context"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramcontroller"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramruntimecomposition"
	"bria/internal/telegramsemantic"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type commandController struct {
	got    telegramcontroller.SemanticAction
	action string
}

func (*commandController) HandleSemanticMessage(context.Context, coordinator.Update) (telegramcontroller.SemanticActionResult, error) {
	return telegramcontroller.SemanticActionResult{}, nil
}
func (c *commandController) HandleSemanticAction(_ context.Context, action telegramcontroller.SemanticAction) (telegramcontroller.SemanticActionResult, error) {
	c.got = action
	return telegramcontroller.SemanticActionResult{Surface: &telegramcontroller.SemanticSurface{Text: "Настройки", RichMarkdown: true, Rows: [][]telegramcontroller.SemanticButton{{{Label: "Настройка", Action: telegramcontroller.SemanticActionKind(c.action)}}}}}, nil
}

func TestCommandLinesPublicAdapterPreservesSemanticAndSettingsSurface(t *testing.T) {
	testPublicSettingsAdapter(t, "settings_technical_command_lines")
}

func TestHiddenDirectoriesPublicAdapterPreservesSemanticAndSettingsSurface(t *testing.T) {
	testPublicSettingsAdapter(t, "settings_hidden_directories")
}

func testPublicSettingsAdapter(t *testing.T, name string) {
	t.Helper()
	action := telegramui.Action(name)
	plan, err := telegrampipeline.PlanAcceptedCallback(telegrampipeline.AcceptedCallback{UpdateID: 1, SessionID: domain.SessionID(telegramui.GlobalSurfaceID), Carrier: telegramstate.Carrier{ChatID: 42, MessageID: 99}, Action: action})
	if err != nil {
		t.Fatal(err)
	}
	c := &commandController{action: name}
	result, err := (telegramruntimecomposition.ControllerFlowAdapter{Controller: c}).HandleCallback(context.Background(), plan)
	if err != nil {
		t.Fatalf("command settings did not reach semantic surface: %v", err)
	}
	if string(c.got.Kind) != string(action) || c.got.SessionID != "" {
		t.Fatalf("semantic=%+v", c.got)
	}
	if err := telegramsemantic.ValidateAction(c.got); err != nil {
		t.Fatal(err)
	}
	if result.Surface == nil || !result.Surface.RichMarkdown || result.Surface.Keyboard.Rows[0][0].Action != action {
		t.Fatalf("surface=%+v", result.Surface)
	}
}
