package telegramcontroller

import (
	"context"
	"errors"
	"strings"

	"bria/internal/domain"
	"bria/internal/settingsport"
	"bria/internal/telegramnodes"
	"bria/internal/telegramsettingsview"
)

func (controller *Controller) cycleTechnicalOutputLines(ctx context.Context) (SemanticActionResult, error) {
	preferences, ok := controller.settings.(settingsport.TechnicalOutputPreferences)
	if !ok {
		return SemanticActionResult{}, errors.New("technical output settings are not configured")
	}
	if err := preferences.CycleTechnicalOutputLines(ctx); err != nil {
		return SemanticActionResult{}, err
	}
	return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryCard)
}

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
			nodeName := string(nodeID)
			if nodes, inventoryErr := controller.nodeInventory(ctx); inventoryErr == nil {
				for _, node := range nodes {
					if node.ID == nodeID && strings.TrimSpace(node.Name) != "" {
						nodeName = node.Name
						break
					}
				}
			}
			provider := "не задан"
			if value := current.DefaultProviders[nodeID]; value != "" {
				provider = authorizationProviderName(value)
			}
			workdir := "не задана"
			if value := strings.TrimSpace(current.DefaultWorkdirs[nodeID]); value != "" {
				workdir = value
			}
			// Contextual values are part of the creation table in the same order
			// as their controls; avoid appending duplicate, unbound rows.
			surface = telegramsettingsview.AppendFields(surface,
				telegramsettingsview.Field{Name: "Нода", Value: nodeName},
				telegramsettingsview.Field{Name: "CLI по умолчанию", Value: provider},
				telegramsettingsview.Field{Name: "Папка по умолчанию", Value: workdir})
			if _, ok := controller.providerPreferences.(settingsport.NodeRenamer); ok {
				surface.Rows = append(surface.Rows[:len(surface.Rows)-1], []telegramsettingsview.Button{{Label: "Переименовать ноду", Action: "settings_rename_node"}}, surface.Rows[len(surface.Rows)-1])
			}
			if hint := controller.StandbyError(nodeID); hint != "" {
				surface = telegramsettingsview.AppendFields(surface, telegramsettingsview.Field{Name: "Ожидающая сессия: ошибка", Value: hint})
			}
		}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: surface.Text, RichMarkdown: surface.RichMarkdown, Rows: rows}}, nil
}
