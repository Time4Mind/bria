package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

type InteractiveInput struct {
	Copy         i18n.Localizer
	Text         string
	Control      bool
	VerticalOnly bool
	Tokens       map[Action]OpaqueToken
}

// RenderInteractiveCard mirrors CCBot's compact keyboard. A view-only share
// can inspect the prompt but cannot emit terminal keys.
func RenderInteractiveCard(input InteractiveInput) Screen {
	rows := make(Grid, 0, 5)
	if input.Control {
		rows = append(rows, Row{
			button("␣ Space", ActionKeySpace, input.Tokens[ActionKeySpace]),
			button("↑", ActionKeyUp, input.Tokens[ActionKeyUp]),
			button("⇥ Tab", ActionKeyTab, input.Tokens[ActionKeyTab]),
		})
		if input.VerticalOnly {
			rows = append(rows, Row{button("↓", ActionKeyDown, input.Tokens[ActionKeyDown])})
		} else {
			rows = append(rows,
				Row{
					button("←", ActionKeyLeft, input.Tokens[ActionKeyLeft]),
					button("↓", ActionKeyDown, input.Tokens[ActionKeyDown]),
					button("→", ActionKeyRight, input.Tokens[ActionKeyRight]),
				},
			)
		}
		rows = append(rows, Row{
			button("⎋ Esc", ActionKeyEscape, input.Tokens[ActionKeyEscape]),
			button("^C", ActionKeyCtrlC, input.Tokens[ActionKeyCtrlC]),
			button("⏎ Enter", ActionKeyEnter, input.Tokens[ActionKeyEnter]),
		})
	} else {
		input.Text += "\n\n" + input.Copy.Text(i18n.CardViewOnly)
	}
	rows = append(rows, Row{
		button(input.Copy.Text(i18n.ButtonBack), ActionKeyBack, input.Tokens[ActionKeyBack]),
		button(input.Copy.Text(i18n.ButtonNewShort), ActionNewSession, ""),
		button(input.Copy.Text(i18n.ButtonMenu), ActionMenu, ""),
	})
	return Screen{Name: ScreenSessionCard, Text: input.Text, Grid: rows}
}
