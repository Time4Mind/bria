package telegramcontroller

import (
	"context"

	"bria/internal/telegramsettings"
)

func (controller *Controller) settingsSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	surface, err := telegramsettings.Render(ctx, controller.settings, controller.providerPreferences, controller.queueLimit)
	if err != nil {
		return SemanticActionResult{}, err
	}
	rows := make([][]SemanticButton, len(surface.Rows))
	for i, row := range surface.Rows {
		rows[i] = make([]SemanticButton, len(row))
		for j, button := range row {
			rows[i][j] = SemanticButton{Label: button.Label, Action: SemanticActionKind(button.Action)}
		}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: surface.Text, Rows: rows}}, nil
}
