package telegramui

import (
	"html"
	"strings"

	"github.com/Time4Mind/bria/internal/i18n"
)

type ClusterHealthFinding struct {
	Severity string
	Title    string
	Evidence string
}

type ClusterHealthInput struct {
	Copy               i18n.Localizer
	Leader             string
	Online             int
	Enabled            int
	CacheEntries       int
	CacheLimit         int
	CacheEvictions     uint64
	TranscriptAverage  string
	TranscriptMaximum  string
	TranscriptTimeouts uint64
	Findings           []ClusterHealthFinding
	AgentAvailable     bool
}

func RenderClusterHealth(input ClusterHealthInput) Screen {
	copy := input.Copy
	leader := input.Leader
	if leader == "" {
		leader = "-"
	}
	summary := copy.Format(
		i18n.ClusterHealthSummary, leader, input.Online, input.Enabled,
		input.CacheEntries, input.CacheLimit, input.CacheEvictions,
		input.TranscriptAverage, input.TranscriptMaximum, input.TranscriptTimeouts,
	)
	lines := []string{
		"<b>" + copy.Text(i18n.ClusterHealthTitle) + "</b>",
		html.EscapeString(summary),
	}
	if len(input.Findings) == 0 {
		lines = append(lines, "", html.EscapeString(copy.Text(i18n.ClusterHealthHealthy)))
	} else {
		lines = append(lines, "")
		for _, finding := range input.Findings {
			icon := "ℹ️"
			switch finding.Severity {
			case "critical":
				icon = "❌"
			case "warning":
				icon = "⚠️"
			}
			line := icon + " " + finding.Title
			if finding.Evidence != "" {
				line += "\n   " + finding.Evidence
			}
			lines = append(lines, html.EscapeString(line))
		}
	}
	rows := Grid{Row{button(copy.Text(i18n.ButtonRefresh), ActionClusterHealthRefresh, "")}}
	if input.AgentAvailable {
		rows = append(rows, Row{button(copy.Text(i18n.ClusterHealthAgent), ActionClusterHealthAgent, "")})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory,
		OpaqueToken(CategoryCluster))})
	return Screen{
		Name: ScreenSettings, ParseMode: ParseModeHTML,
		Text: strings.Join(lines, "\n"), Grid: rows,
	}
}
