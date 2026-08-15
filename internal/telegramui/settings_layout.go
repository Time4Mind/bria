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
		button(copy.Text(i18n.SettingsVoice), ActionSettingsCategory, OpaqueToken(CategoryVoice)),
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
	if category == CategoryCluster {
		rows = append(rows, clusterSettingsActions(input)...)
	}
	items := make([]Button, 0, len(settingsIn(category)))
	for _, descriptor := range settingsIn(category) {
		label := copy.Text(descriptor.label) + ": " + settingValue(input, descriptor.id)
		items = append(items, button(label, ActionOpenSetting, OpaqueToken(descriptor.id)))
	}
	rows = append(rows, settingsRows(items)...)
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettings, "")})
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: settingsCategoryText(input, category),
		Grid: rows,
	}, nil
}

// settingsRows keeps long settings menus compact without coupling their
// semantic grouping to Telegram's keyboard representation. A trailing odd
// button remains full-width on its own row.
func settingsRows(buttons []Button) Grid {
	rows := make(Grid, 0, (len(buttons)+1)/2)
	for index := 0; index < len(buttons); index += 2 {
		end := min(index+2, len(buttons))
		rows = append(rows, Row(buttons[index:end]))
	}
	return rows
}
