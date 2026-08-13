package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

// RenderMainMenu preserves CCBot's exact five-button order and 2/2/1 grid.
func RenderMainMenu(copy i18n.Localizer, activeSessionName string) Screen {
	text := copy.Text(i18n.MenuTitle)
	if activeSessionName != "" {
		text += " · " + copy.Format(i18n.MenuActive, activeSessionName)
	}
	return Screen{
		Name: ScreenMenu,
		Text: text,
		Grid: Grid{
			Row{
				button(copy.Text(i18n.ButtonSessions), ActionSessions, ""),
				button(copy.Text(i18n.ButtonArchive), ActionArchive, ""),
			},
			Row{
				button(copy.Text(i18n.ButtonStatus), ActionStatus, ""),
				button(copy.Text(i18n.ButtonNew), ActionNewSession, ""),
			},
			Row{button(copy.Text(i18n.ButtonSettings), ActionSettings, "")},
		},
	}
}
