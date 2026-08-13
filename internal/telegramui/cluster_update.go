package telegramui

import (
	"fmt"
	"html"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

func clusterSettingsActions(input SettingsInput) Grid {
	rows := Grid{
		Row{button(input.Copy.Text(i18n.ClusterUpdateButton), ActionClusterUpdate, "")},
		Row{button(input.Copy.Text(i18n.ClusterAddNode), ActionClusterAdd, "")},
	}
	for _, pending := range input.PendingEnrollments {
		rows = append(rows, Row{button("⏳ "+pending.Name, ActionEnrollmentOpen, pending.Token)})
	}
	return rows
}

func RenderClusterUpdateConfirmation(copy i18n.Localizer) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.ClusterUpdateTitle) + "</b>\n\n" +
			html.EscapeString(copy.Text(i18n.ClusterUpdateConfirm)),
		Grid: Grid{
			Row{button(copy.Text(i18n.ButtonConfirm), ActionClusterUpdateYes, "")},
			Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory, OpaqueToken(CategoryCluster))},
		},
	}
}

func RenderClusterUpdateUnavailable(copy i18n.Localizer) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.ClusterUpdateTitle) + "</b>\n\n" +
			html.EscapeString(copy.Text(i18n.ClusterUpdateUnavailable)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory,
			OpaqueToken(CategoryCluster))}},
	}
}

func RenderClusterUpdateError(copy i18n.Localizer, detail string) Screen {
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: "<b>" + copy.Text(i18n.ClusterUpdateTitle) + "</b>\n\n" +
			html.EscapeString(copy.Format(i18n.ClusterUpdateFailed, detail)),
		Grid: Grid{
			Row{button(copy.Text(i18n.ClusterUpdateButton), ActionClusterUpdate, "")},
			Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory, OpaqueToken(CategoryCluster))},
		},
	}
}

func RenderClusterUpdate(
	copy i18n.Localizer, update domain.ClusterUpdate, nodes map[domain.NodeID]domain.Node,
) Screen {
	var summary string
	switch update.Phase {
	case domain.ClusterUpdateCompleted:
		summary = copy.Format(i18n.ClusterUpdateCompleted, update.Version)
	case domain.ClusterUpdateFailed:
		summary = copy.Format(i18n.ClusterUpdateFailed, update.Error)
	default:
		summary = copy.Format(i18n.ClusterUpdateRunning, update.Version)
	}
	lines := []string{"<b>" + copy.Text(i18n.ClusterUpdateTitle) + "</b>", html.EscapeString(summary)}
	for _, nodeID := range update.Order {
		status := update.Nodes[nodeID]
		icon := "⏳"
		switch status.Phase {
		case domain.NodeUpdateInstalling:
			icon = "⬇️"
		case domain.NodeUpdateHealthy:
			icon = "✅"
		case domain.NodeUpdateFailed:
			icon = "❌"
		}
		name := nodes[nodeID].Name
		if name == "" {
			name = string(nodeID)
		}
		line := fmt.Sprintf("%s %s · %s", icon, name, status.Phase)
		if status.Error != "" {
			line += " · " + status.Error
		}
		lines = append(lines, html.EscapeString(line))
	}
	rows := Grid{}
	if update.Active() {
		rows = append(rows, Row{button(copy.Text(i18n.ButtonRefresh), ActionClusterUpdateRefresh, "")})
	} else {
		rows = append(rows, Row{button(copy.Text(i18n.ClusterUpdateButton), ActionClusterUpdate, "")})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory,
		OpaqueToken(CategoryCluster))})
	return Screen{Name: ScreenSettings, ParseMode: ParseModeHTML, Text: strings.Join(lines, "\n"), Grid: rows}
}
