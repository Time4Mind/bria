package telegramui

import (
	"fmt"

	"github.com/Time4Mind/bria/internal/i18n"
)

type SharedAccess string

const (
	SharedView    SharedAccess = "view"
	SharedControl SharedAccess = "control"
)

type CardInput struct {
	Copy               i18n.Localizer
	Text               string
	RichMarkdown       bool
	Access             SharedAccess
	Owner              bool
	Busy               bool
	Starting           bool
	HidePagination     bool
	Page               int
	Pages              int
	CanOpenTerminal    bool
	CanRestore         bool
	AcceptsQueuedInput bool
	Tokens             map[Action]OpaqueToken
	Sessions           []SessionItem
	AllHosts           bool
}

// RenderSessionCard keeps CCBot's pagination, control and bottom rows. A
// view-only share retains navigation but never renders session controls.
func RenderSessionCard(input CardInput) Screen {
	copy := input.Copy
	page, pages := normalizedPages(input.Page, input.Pages)
	rows := make(Grid, 0, 3+(len(input.Sessions)+sessionsPerRow-1)/sessionsPerRow)
	if !input.HidePagination {
		rows = append(rows, Row{
			button("◀", ActionPagePrevious, input.Tokens[ActionPagePrevious]),
			button(pageLabel(page, pages), ActionPageLatest, input.Tokens[ActionPageLatest]),
			button("▶", ActionPageNext, input.Tokens[ActionPageNext]),
		})
	}
	if input.Access == SharedControl {
		firstLabel, firstAction := copy.Text(i18n.ButtonClose), ActionClose
		if input.Busy {
			firstLabel, firstAction = copy.Text(i18n.ButtonStop), ActionStop
		}
		controls := Row{
			button(firstLabel, firstAction, input.Tokens[firstAction]),
		}
		if input.Owner {
			if !input.Starting {
				controls = append(controls,
					button(copy.Text(i18n.ButtonClear), ActionClear, input.Tokens[ActionClear]))
				if input.CanOpenTerminal {
					controls = append(controls,
						button(copy.Text(i18n.ButtonTerminal), ActionTerminal, input.Tokens[ActionTerminal]))
				}
			}
		} else if !input.Busy {
			// A controller may interrupt a running turn but lifecycle remains
			// owner-only. An idle shared card therefore has no control row.
			controls = nil
		}
		if len(controls) > 0 {
			rows = append(rows, controls)
		}
	} else if input.Access != SharedControl && !input.CanRestore && !input.AcceptsQueuedInput {
		// Unknown access fails closed and renders the same read-only surface as
		// an explicit view grant. Application authorization remains mandatory.
		input.Text += "\n\n" + copy.Text(i18n.CardViewOnly)
	}
	if input.CanRestore {
		rows = append(rows, Row{
			button(copy.Text(i18n.ButtonRestore), ActionRestore, input.Tokens[ActionRestore]),
		})
	}
	rows = append(rows, sessionSwitcherRows(input.Sessions, input.AllHosts)...)
	rows = append(rows, Row{
		button(copy.Text(i18n.ButtonNewShort), ActionNewSession, ""),
		button(copy.Text(i18n.ButtonServers), ActionSessions, "servers"),
		button(copy.Text(i18n.ButtonMenu), ActionMenu, ""),
	})
	return Screen{
		Name: ScreenSessionCard, Text: input.Text, Grid: rows,
		RichMarkdown: input.RichMarkdown,
	}
}

func sessionSwitcherRows(items []SessionItem, allHosts bool) Grid {
	rows := make(Grid, 0, (len(items)+sessionsPerRow-1)/sessionsPerRow)
	for index, item := range items {
		if index%sessionsPerRow == 0 {
			rows = append(rows, Row{})
		}
		label := item.Name
		if allHosts {
			label = allHostSessionLabel(item)
		} else {
			label = selectedSessionLabel(item)
		}
		rows[len(rows)-1] = append(rows[len(rows)-1],
			button(label, ActionSelectSession, item.Token))
	}
	return rows
}

func normalizedPages(page, pages int) (int, int) {
	if pages < 1 {
		pages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	return page, pages
}

func pageLabel(page, pages int) string {
	return fmt.Sprintf("%d/%d", page, pages)
}
