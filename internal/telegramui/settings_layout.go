package telegramui

import (
	"fmt"

	"github.com/Time4Mind/bria/internal/i18n"
)

func RenderSettings(input SettingsInput) Screen {
	copy := input.Copy
	categories := []Button{
		button(copy.Text(i18n.SettingsInterface), ActionSettingsCategory, OpaqueToken(CategoryInterface)),
		button(copy.Text(i18n.SettingsCard), ActionSettingsCategory, OpaqueToken(CategoryCard)),
		button(copy.Text(i18n.SettingsArchive), ActionSettingsCategory, OpaqueToken(CategoryArchive)),
		button(copy.Text(i18n.SettingsNotifications), ActionSettingsCategory, OpaqueToken(CategoryNotifications)),
		button(copy.Text(i18n.SettingsCluster), ActionSettingsCategory, OpaqueToken(CategoryCluster)),
	}
	rows := settingsRows(categories)
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionMenu, "")})
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.SettingsTitle) + "</b>\n\n" + copy.Text(i18n.SettingsBody),
		Grid: rows,
	}
}

func RenderSettingsCategory(input SettingsInput, category SettingsCategory) (Screen, error) {
	if !validSettingsCategory(category) {
		return Screen{}, fmt.Errorf("unknown settings category: %q", category)
	}
	copy := input.Copy
	rows := make(Grid, 0, len(settingsIn(category))+1)
	items := make([]Button, 0, len(settingsIn(category)))
	for _, descriptor := range settingsIn(category) {
		label := copy.Text(descriptor.label) + ": " + settingValue(input, descriptor.id)
		items = append(items, button(label, ActionOpenSetting, OpaqueToken(descriptor.id)))
	}
	rows = append(rows, settingsRows(items)...)
	if category == CategoryCluster {
		rows = append(rows, clusterSettingsActions(input)...)
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettings, "")})
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: settingsCategoryText(input, category),
		Grid: rows,
	}, nil
}

// Settings values must remain readable on narrow mobile screens. Each row is
// therefore a full-width choice; compactness comes from semantic grouping,
// not from truncating two unrelated selectors into one Telegram row.
func settingsRows(buttons []Button) Grid {
	rows := make(Grid, 0, len(buttons))
	for _, item := range buttons {
		rows = append(rows, Row{item})
	}
	return rows
}
