package telegramui

import (
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/i18n"
)

const sessionsPerRow = 3

type SessionItem struct {
	Token      OpaqueToken
	Name       string
	NodeName   string
	Marker     string
	Status     string
	ContextPct *int
	NeedsInput bool
	Selected   bool
}

// RenderAllHostSessions keeps CCBot's three-session row density. Every label
// includes its node because no surrounding host context exists in this mode.
func RenderAllHostSessions(copy i18n.Localizer, items []SessionItem) Screen {
	return RenderAllHostSessionsPage(copy, items, 1, 1, "", "")
}

func RenderAllHostSessionsPage(
	copy i18n.Localizer,
	items []SessionItem,
	page, pages int,
	previous, next OpaqueToken,
) Screen {
	page, pages = normalizedPages(page, pages)
	rows := make(Grid, 0, (len(items)+sessionsPerRow-1)/sessionsPerRow+2)
	for index, item := range items {
		if index%sessionsPerRow == 0 {
			rows = append(rows, Row{})
		}
		rows[len(rows)-1] = append(
			rows[len(rows)-1],
			button(allHostSessionLabel(item), ActionSelectSession, item.Token),
		)
	}
	if pages > 1 {
		rows = append(rows, listNavigation(page, pages, previous, next,
			ActionSessionsPrevious, ActionSessionsNext))
	}
	rows = append(rows, Row{
		button(copy.Text(i18n.ButtonNew), ActionNewSession, ""),
		button(copy.Text(i18n.ButtonServers), ActionSessions, "servers"),
		button(copy.Text(i18n.ButtonMenu), ActionMenu, ""),
	})
	return Screen{
		Name: ScreenSessions,
		Text: copy.Text(i18n.AllSessionsTitle),
		Grid: rows,
	}
}

// RenderNodeSessions is the host-first fallback when a selected node has no
// active card to open yet. Nodes with an active session open that card directly.
func RenderNodeSessions(copy i18n.Localizer, node NodeItem, items []SessionItem) Screen {
	return RenderNodeSessionsPage(copy, node, items, 1, 1, "", "")
}

func RenderNodeSessionsPage(
	copy i18n.Localizer,
	node NodeItem,
	items []SessionItem,
	page, pages int,
	previous, next OpaqueToken,
) Screen {
	page, pages = normalizedPages(page, pages)
	rows := make(Grid, 0, (len(items)+sessionsPerRow-1)/sessionsPerRow+2)
	for index, item := range items {
		if index%sessionsPerRow == 0 {
			rows = append(rows, Row{})
		}
		rows[len(rows)-1] = append(
			rows[len(rows)-1],
			button(selectedSessionLabel(item), ActionSelectSession, item.Token),
		)
	}
	text := copy.Format(i18n.NodeLiveTitle, node.Name)
	if node.Status == NodeOffline {
		text = node.Name + " · " + copy.Text(i18n.NodeUnavailable)
	}
	if pages > 1 {
		rows = append(rows, listNavigation(page, pages, previous, next,
			ActionSessionsPrevious, ActionSessionsNext))
	}
	actions := Row{}
	if node.Status != NodeOffline && node.NewToken != "" {
		actions = append(actions, button(copy.Text(i18n.ButtonNew), ActionNewSession, node.NewToken))
	}
	actions = append(actions, button(copy.Text(i18n.ButtonServers), ActionSessions, "servers"))
	actions = append(actions, button(copy.Text(i18n.ButtonMenu), ActionMenu, ""))
	rows = append(rows, actions)
	return Screen{Name: ScreenSessions, Text: text, Grid: rows}
}

func listNavigation(
	page, pages int,
	previous, next OpaqueToken,
	previousAction, nextAction Action,
) Row {
	row := Row{}
	if previous != "" {
		row = append(row, button("◀", previousAction, previous))
	}
	row = append(row, button(pageLabel(page, pages), ActionNoop, ""))
	if next != "" {
		row = append(row, button("▶", nextAction, next))
	}
	return row
}

// RenderUnavailableNode keeps an offline node selectable without presenting
// its cached sessions as live/background work. The last card and archive remain
// explicit read surfaces.
func RenderUnavailableNode(copy i18n.Localizer, node NodeItem, last *SessionItem) Screen {
	rows := make(Grid, 0, 3)
	if last != nil {
		rows = append(rows, Row{
			button(copy.Format(i18n.LastCard, last.Name), ActionSelectSession, last.Token),
		})
	}
	rows = append(rows,
		Row{button(copy.Text(i18n.ButtonArchive), ActionArchive, "")},
		Row{button(copy.Text(i18n.ButtonBackServers), ActionSessions, "servers")},
	)
	return Screen{
		Name: ScreenSessions,
		Text: node.Name + " · " + copy.Text(i18n.NodeUnavailable),
		Grid: rows,
	}
}

func allHostSessionLabel(item SessionItem) string {
	parts := make([]string, 0, 7)
	if item.Selected {
		parts = append(parts, "✓")
	}
	if item.Marker != "" {
		parts = append(parts, item.Marker)
	}
	parts = append(parts, item.Name+" · "+item.NodeName, item.Status)
	if item.ContextPct != nil {
		parts = append(parts, "·", fmt.Sprintf("%d%%", *item.ContextPct))
	}
	return strings.Join(parts, " ")
}

func selectedSessionLabel(item SessionItem) string {
	parts := make([]string, 0, 4)
	if item.Selected {
		parts = append(parts, "✓")
	}
	parts = append(parts, item.Name, item.Status)
	if item.ContextPct != nil {
		parts = append(parts, "·", fmt.Sprintf("%d%%", *item.ContextPct))
	}
	return strings.Join(nonEmpty(parts), " ")
}
