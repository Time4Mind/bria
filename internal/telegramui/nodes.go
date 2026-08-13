package telegramui

import (
	"fmt"

	"github.com/Time4Mind/bria/internal/i18n"
)

type NodeStatus string

const (
	NodeOnline       NodeStatus = "online"
	NodeReconnecting NodeStatus = "reconnecting"
	NodeOffline      NodeStatus = "offline"
)

type NodeItem struct {
	Token         OpaqueToken
	Name          string
	Status        NodeStatus
	LiveSessions  int
	Selected      bool
	SettingsToken OpaqueToken
	NewToken      OpaqueToken
}

// RenderHostFirstNodes is the only extra navigation screen inserted before
// CCBot's normal session surface in host_first mode.
func RenderHostFirstNodes(copy i18n.Localizer, items []NodeItem) Screen {
	return RenderHostFirstNodesPage(copy, items, 1, 1, "", "")
}

func RenderHostFirstNodesPage(
	copy i18n.Localizer,
	items []NodeItem,
	page, pages int,
	previous, next OpaqueToken,
) Screen {
	page, pages = normalizedPages(page, pages)
	rows := make(Grid, 0, len(items)+2)
	for _, item := range items {
		selected := ""
		if item.Selected {
			selected = "✓ "
		}
		label := fmt.Sprintf("%s %s%s (%d)", nodeStatusGlyph(item.Status),
			selected, item.Name, item.LiveSessions)
		if item.Status == NodeOffline {
			label = fmt.Sprintf("%s %s%s · %s", nodeStatusGlyph(item.Status), selected,
				item.Name, copy.Text(i18n.NodeUnavailable))
		}
		rows = append(rows, Row{button(label, ActionSelectNode, item.Token)})
	}
	if len(items) == 0 {
		rows = append(rows, Row{button(copy.Text(i18n.NoServers), ActionNoop, "")})
	}
	if pages > 1 {
		rows = append(rows, listNavigation(page, pages, previous, next,
			ActionNodesPrevious, ActionNodesNext))
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSessions, "")})
	return Screen{
		Name: ScreenNodes,
		Text: copy.Text(i18n.NodesTitle) + "\n\n" + copy.Text(i18n.NodesBody),
		Grid: rows,
	}
}

func nodeStatusGlyph(status NodeStatus) string {
	switch status {
	case NodeOnline:
		return "🟢"
	case NodeReconnecting:
		return "🟡"
	default:
		return "🔴"
	}
}
