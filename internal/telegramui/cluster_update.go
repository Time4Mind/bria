package telegramui

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

type NodeUpdateProgress struct {
	Phase    string
	Progress int
	Error    string
}

func clusterSettingsActions(input SettingsInput) Grid {
	buttons := []Button{
		button(input.Copy.Text(i18n.ClusterHealthButton), ActionClusterHealth, ""),
		button(input.Copy.Text(i18n.ClusterUpdateButton), ActionClusterUpdate, ""),
		button(input.Copy.Text(i18n.ClusterAddNode), ActionClusterAdd, ""),
	}
	for _, pending := range input.PendingEnrollments {
		buttons = append(buttons, button("⏳ "+pending.Name, ActionEnrollmentOpen, pending.Token))
	}
	return settingsRows(buttons)
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
	return RenderClusterUpdateProgress(copy, update, nodes, nil, time.Now())
}

func RenderClusterUpdateProgress(
	copy i18n.Localizer,
	update domain.ClusterUpdate,
	nodes map[domain.NodeID]domain.Node,
	live map[domain.NodeID]NodeUpdateProgress,
	now time.Time,
) Screen {
	offline := offlineUpdateNodes(update, nodes)
	var summary string
	switch update.Phase {
	case domain.ClusterUpdateCompleted:
		if len(offline) > 0 {
			summary = copy.Format(i18n.ClusterUpdateCompletedPartial, update.Version)
		} else {
			summary = copy.Format(i18n.ClusterUpdateCompleted, update.Version)
		}
	case domain.ClusterUpdateFailed:
		summary = copy.Format(i18n.ClusterUpdateFailed, update.Error)
	default:
		summary = copy.Format(i18n.ClusterUpdateRunning, update.Version)
	}
	progress, completed := aggregateUpdateProgress(update, live)
	elapsed := time.Duration(0)
	if !update.StartedAt.IsZero() {
		elapsed = max(now.Sub(update.StartedAt), 0)
	}
	progressLine := copy.Format(
		i18n.ClusterUpdateProgress, progress, completed, len(update.Order), compactDuration(elapsed),
	)
	if eta, ok := updateETA(update, live, now); ok && update.Active() {
		progressLine += copy.Format(i18n.ClusterUpdateETA, compactDuration(eta))
	}
	lines := []string{
		"<b>" + copy.Text(i18n.ClusterUpdateTitle) + "</b>", html.EscapeString(summary),
		progressBar(progress), html.EscapeString(progressLine),
	}
	for _, nodeID := range update.Order {
		status := update.Nodes[nodeID]
		nodeProgress := progressForNode(status, live[nodeID])
		icon := progressIcon(nodeProgress)
		name := nodes[nodeID].Name
		if name == "" {
			name = string(nodeID)
		}
		from := status.PreviousVersion
		if from == "" {
			from = nodes[nodeID].Version
		}
		versions := strings.TrimSpace(from) + " → " + update.Version
		line := fmt.Sprintf("%s %s · %d%% · %s · %s",
			icon, name, nodeProgress.Progress, updatePhaseText(copy, nodeProgress.Phase), versions)
		detail := nodeProgress.Error
		if detail == "" {
			detail = status.Error
		}
		if detail != "" {
			line += " · " + detail
		}
		lines = append(lines, html.EscapeString(line))
	}
	if len(offline) > 0 {
		lines = append(lines, "", html.EscapeString(copy.Format(i18n.ClusterUpdateWaitingOffline, len(offline))))
		for _, node := range offline {
			lines = append(lines, html.EscapeString(fmt.Sprintf(
				"○ %s · 0%% · %s · %s → %s", node.Name,
				copy.Text(i18n.ClusterUpdatePhaseWaiting), node.Version, update.Version,
			)))
		}
	}
	rows := Grid{}
	if update.Active() {
		rows = append(rows, Row{button(copy.Text(i18n.ButtonRefresh), ActionClusterUpdateRefresh, "")})
	} else if update.Phase == domain.ClusterUpdateFailed {
		rows = append(rows, Row{button(copy.Text(i18n.ClusterUpdateRetry), ActionClusterUpdateRetry, "")})
	} else {
		rows = append(rows, Row{button(copy.Text(i18n.ClusterUpdateButton), ActionClusterUpdate, "")})
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), ActionSettingsCategory,
		OpaqueToken(CategoryCluster))})
	return Screen{Name: ScreenSettings, ParseMode: ParseModeHTML, Text: strings.Join(lines, "\n"), Grid: rows}
}

func progressForNode(status domain.NodeUpdate, live NodeUpdateProgress) NodeUpdateProgress {
	if live.Phase != "" {
		live.Progress = min(max(live.Progress, 0), 100)
		return live
	}
	switch status.Phase {
	case domain.NodeUpdateHealthy:
		return NodeUpdateProgress{Phase: "healthy", Progress: 100}
	case domain.NodeUpdateFailed:
		return NodeUpdateProgress{Phase: "failed", Error: status.Error}
	case domain.NodeUpdateInstalling:
		return NodeUpdateProgress{Phase: "downloading", Progress: 50}
	default:
		return NodeUpdateProgress{Phase: "waiting"}
	}
}

func aggregateUpdateProgress(
	update domain.ClusterUpdate, live map[domain.NodeID]NodeUpdateProgress,
) (int, int) {
	if len(update.Order) == 0 {
		return 0, 0
	}
	total, completed := 0, 0
	for _, nodeID := range update.Order {
		progress := progressForNode(update.Nodes[nodeID], live[nodeID]).Progress
		total += progress
		if progress == 100 {
			completed++
		}
	}
	return total / len(update.Order), completed
}

func updateETA(
	update domain.ClusterUpdate,
	live map[domain.NodeID]NodeUpdateProgress,
	now time.Time,
) (time.Duration, bool) {
	var completedTotal time.Duration
	completed := 0
	for _, nodeID := range update.Order {
		status := update.Nodes[nodeID]
		if status.Phase == domain.NodeUpdateHealthy && !status.StartedAt.IsZero() &&
			!status.CompletedAt.Before(status.StartedAt) {
			completedTotal += status.CompletedAt.Sub(status.StartedAt)
			completed++
		}
	}
	average := time.Duration(0)
	if completed > 0 {
		average = completedTotal / time.Duration(completed)
	} else {
		for _, nodeID := range update.Order {
			status := update.Nodes[nodeID]
			progress := progressForNode(status, live[nodeID]).Progress
			if status.Phase == domain.NodeUpdateInstalling && progress >= 5 && !status.StartedAt.IsZero() {
				average = now.Sub(status.StartedAt) * 100 / time.Duration(progress)
				break
			}
		}
	}
	if average <= 0 {
		return 0, false
	}
	remaining := time.Duration(0)
	for _, nodeID := range update.Order {
		progress := progressForNode(update.Nodes[nodeID], live[nodeID]).Progress
		remaining += average * time.Duration(100-progress) / 100
	}
	return max(remaining, 0), true
}

func offlineUpdateNodes(
	update domain.ClusterUpdate, nodes map[domain.NodeID]domain.Node,
) []domain.Node {
	included := make(map[domain.NodeID]bool, len(update.Order))
	for _, nodeID := range update.Order {
		included[nodeID] = true
	}
	result := make([]domain.Node, 0)
	for nodeID, node := range nodes {
		if node.Enabled() && !included[nodeID] {
			result = append(result, node)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func updatePhaseText(copy i18n.Localizer, phase string) string {
	switch phase {
	case "inspecting":
		return copy.Text(i18n.ClusterUpdatePhaseInspecting)
	case "downloading":
		return copy.Text(i18n.ClusterUpdatePhaseDownloading)
	case "verifying":
		return copy.Text(i18n.ClusterUpdatePhaseVerifying)
	case "extracting":
		return copy.Text(i18n.ClusterUpdatePhaseExtracting)
	case "preflight":
		return copy.Text(i18n.ClusterUpdatePhasePreflight)
	case "activating":
		return copy.Text(i18n.ClusterUpdatePhaseActivating)
	case "restarting", "staged":
		return copy.Text(i18n.ClusterUpdatePhaseRestarting)
	case "healthy":
		return copy.Text(i18n.ClusterUpdatePhaseHealthy)
	case "failed":
		return copy.Text(i18n.ClusterUpdatePhaseFailed)
	default:
		return copy.Text(i18n.ClusterUpdatePhaseWaiting)
	}
}

func progressIcon(progress NodeUpdateProgress) string {
	switch progress.Phase {
	case "healthy":
		return "✅"
	case "failed":
		return "❌"
	case "restarting", "staged":
		return "↻"
	case "waiting":
		return "○"
	default:
		return "⬇️"
	}
}

func progressBar(progress int) string {
	filled := min(max(progress, 0), 100) / 10
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled) + fmt.Sprintf(" %d%%", progress)
}

func compactDuration(duration time.Duration) string {
	duration = duration.Round(time.Second)
	if duration < time.Minute {
		return fmt.Sprintf("%ds", max(int(duration.Seconds()), 0))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm %02ds", int(duration.Minutes()), int(duration.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(duration.Hours()), int(duration.Minutes())%60)
}
