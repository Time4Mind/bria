package telegramcontroller

import (
	"context"
	"strings"

	"bria/internal/domain"
	"bria/internal/settingsport"
	"bria/internal/telegramnodes"
	"bria/internal/telegramsettingsview"
)

func (controller *Controller) settingsSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	surface := telegramsettingsview.Render()
	return controller.projectSettingsSurface(ctx, surface, 0, nil)
}

func (controller *Controller) settingsCategorySemanticResult(ctx context.Context, category telegramsettingsview.Category) (SemanticActionResult, error) {
	surface, err := telegramsettingsview.RenderCategory(ctx, controller.settings, controller.scopedProviderPreferences(), controller.queueLimit, category)
	return controller.projectSettingsSurface(ctx, surface, category, err)
}

func (controller *Controller) scopedProviderPreferences() settingsport.ProviderPreferences {
	if controller.providerPreferences == nil && controller.creationEnvironment == nil {
		return nil
	}
	return telegramnodes.ProviderPreferences{Current: controller.currentNodeID, LocalNode: controller.localComputerID,
		Local: controller.providerPreferences, Environment: controller.creationEnvironment}
}

func (controller *Controller) toggleNodeProvider(ctx context.Context, nodeID domain.ComputerID, provider domain.Provider) error {
	preferences := telegramnodes.ProviderPreferences{Current: controller.currentNodeID, LocalNode: controller.localComputerID,
		Local: controller.providerPreferences, Environment: controller.creationEnvironment}
	return preferences.ToggleAt(ctx, nodeID, provider)
}

func (controller *Controller) projectSettingsSurface(ctx context.Context, surface telegramsettingsview.Surface, category telegramsettingsview.Category, err error) (SemanticActionResult, error) {
	if err != nil {
		return SemanticActionResult{}, err
	}
	rows := make([][]SemanticButton, len(surface.Rows))
	for i, row := range surface.Rows {
		rows[i] = make([]SemanticButton, len(row))
		for j, button := range row {
			rows[i][j] = SemanticButton{Label: button.Label, Action: SemanticActionKind(button.Action), Choice: button.Choice}
		}
	}
	if category == telegramsettingsview.CategoryCreation && controller.settings != nil {
		if current, snapshotErr := controller.settings.Snapshot(ctx); snapshotErr == nil {
			nodeID := controller.currentNodeID()
			provider := "не задан"
			if value := current.DefaultProviders[nodeID]; value != "" {
				provider = authorizationProviderName(value)
			}
			workdir := "не задана"
			if value := strings.TrimSpace(current.DefaultWorkdirs[nodeID]); value != "" {
				workdir = value
			}
			surface.Text += "\nНода: " + string(nodeID) +
				"\nBackend по умолчанию (" + string(nodeID) + "): " + provider +
				"\nПапка по умолчанию: " + workdir
		}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: surface.Text, Rows: rows}}, nil
}
