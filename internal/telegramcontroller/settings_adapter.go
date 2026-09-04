package telegramcontroller

import (
	"context"
	"strings"

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
	if controller.settings != nil {
		if current, snapshotErr := controller.settings.Snapshot(ctx); snapshotErr == nil {
			provider := "не задан"
			if value := current.DefaultProviders[controller.localComputerID]; value != "" {
				provider = authorizationProviderName(value)
			}
			workdir := "не задана"
			if value := strings.TrimSpace(current.DefaultWorkdirs[controller.localComputerID]); value != "" {
				workdir = value
			}
			surface.Text += "\nBackend по умолчанию (" + string(controller.localComputerID) + "): " + provider +
				"\nПапка по умолчанию: " + workdir
		}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: surface.Text, Rows: rows}}, nil
}
