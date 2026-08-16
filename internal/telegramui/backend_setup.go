package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

type NodeBackendItem struct {
	Name      string
	Version   string
	Installed bool
	Connected bool
	Token     OpaqueToken
}

func RenderBackendSetup(
	copy i18n.Localizer,
	text string,
	retry OpaqueToken,
	back OpaqueToken,
) Screen {
	rows := Grid{}
	if retry != "" {
		rows = append(rows, Row{button(copy.Text(i18n.BackendInstall), ActionBackendInstall, retry)})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionNodeSettings, back)})
	return Screen{Name: ScreenStatus, Text: text, ParseMode: ParseModeHTML, Grid: rows}
}
