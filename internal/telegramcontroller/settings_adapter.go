package telegramcontroller

import (
	"context"
	"slices"
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
	providers := controller.scopedProviderPreferences()
	providerUnavailable := false
	surface, err := telegramsettingsview.RenderCategory(ctx, controller.settings, providers, controller.queueLimit, category)
	if err != nil && category == telegramsettingsview.CategoryProviders && providers != nil {
		// RenderCategory is the single live inventory read. If that read fails,
		// render a degraded page without querying the provider a second time.
		providerUnavailable = true
		surface, err = telegramsettingsview.RenderCategory(ctx, controller.settings, nil, controller.queueLimit, category)
	}
	if err == nil && providerUnavailable {
		surface = telegramsettingsview.AppendFields(surface, telegramsettingsview.Field{Name: "Статус CLI", Value: "временно недоступен"})
	}
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
	if category == telegramsettingsview.CategoryCreation && controller.settings != nil {
		if current, snapshotErr := controller.settings.Snapshot(ctx); snapshotErr == nil {
			nodeID := controller.currentNodeID()
			nodeName := string(nodeID)
			if reader, ok := controller.providerPreferences.(settingsport.NodeNameReader); ok {
				if currentName, nameErr := reader.NodeName(ctx, nodeID); nameErr == nil && strings.TrimSpace(currentName) != "" {
					nodeName = currentName
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
			surface.Text = withoutSettingsFields(surface.Text, "CLI по умолчанию", "Папка по умолчанию")
			for rowIndex := range surface.Rows {
				for buttonIndex := range surface.Rows[rowIndex] {
					if surface.Rows[rowIndex][buttonIndex].Action == "settings_default_provider" {
						surface.Rows[rowIndex][buttonIndex].Label = "CLI по умолчанию"
					}
				}
			}
			// Contextual values are part of the creation table in the same order
			// as their controls; avoid duplicate, node-agnostic defaults.
			surface = telegramsettingsview.AppendFields(surface,
				telegramsettingsview.Field{Name: "Нода", Value: nodeName},
				telegramsettingsview.Field{Name: "CLI по умолчанию", Value: provider},
				telegramsettingsview.Field{Name: "Папка по умолчанию", Value: workdir})
			if _, ok := controller.providerPreferences.(settingsport.NodeRenamer); ok && nodeID == controller.localComputerID {
				surface.Rows = append(surface.Rows[:len(surface.Rows)-1], []telegramsettingsview.Button{{Label: "Переименовать ноду", Action: "settings_rename_node"}}, surface.Rows[len(surface.Rows)-1])
			}
			if hint := controller.StandbyError(nodeID); hint != "" {
				surface = telegramsettingsview.AppendFields(surface, telegramsettingsview.Field{Name: "Ожидающая сессия: ошибка", Value: hint})
			}
		}
	}
	rows := make([][]SemanticButton, len(surface.Rows))
	for i, row := range surface.Rows {
		rows[i] = make([]SemanticButton, len(row))
		for j, button := range row {
			rows[i][j] = SemanticButton{Label: button.Label, Action: SemanticActionKind(button.Action), Choice: button.Choice}
		}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: surface.Text, RichMarkdown: surface.RichMarkdown, Rows: rows}}, nil
}

func withoutSettingsFields(text string, names ...string) string {
	lines := strings.Split(text, "\n")
	lines = slices.DeleteFunc(lines, func(line string) bool {
		return slices.ContainsFunc(names, func(name string) bool {
			return strings.HasPrefix(strings.TrimSpace(line), "| "+name+" |")
		})
	})
	return strings.Join(lines, "\n")
}
