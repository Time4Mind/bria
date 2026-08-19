package telegramui

import (
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

type StatusMode string

const (
	StatusChoose   StatusMode = "choose"
	StatusLeader   StatusMode = "leader"
	StatusSettings StatusMode = "settings"
)

type StatusItem struct {
	Token         OpaqueToken
	Name          string
	Status        NodeStatus
	Leader        bool
	PinnedMinutes int
	ObservedAt    time.Time
	Quotas        []domain.QuotaSnapshot
	Disabled      bool
}

type StatusInput struct {
	Copy       i18n.Localizer
	Mode       StatusMode
	Now        time.Time
	Items      []StatusItem
	BackAction Action
	BackToken  OpaqueToken
}

func RenderStatus(input StatusInput) Screen {
	copy := input.Copy
	mode := input.Mode
	if mode != StatusSettings {
		mode = StatusChoose
	}
	text := copy.Text(i18n.StatusTitle)
	if mode == StatusChoose {
		text += "\n\n" + statusTable(copy, input.Items, input.Now)
	} else {
		text += "\n\n" + copy.Text(i18n.StatusSettingsBody)
	}
	rows := Grid{
		Row{button(copy.Text(i18n.ButtonRefresh), ActionStatusRefresh, OpaqueToken(mode))},
		Row{
			button(selectedLabel(mode == StatusChoose, copy.Text(i18n.StatusModeChoose)), ActionStatusMode, "choose"),
			button(selectedLabel(mode == StatusSettings, copy.Text(i18n.StatusModeSettings)), ActionStatusMode, "settings"),
		},
	}
	disabledStarted := false
	for _, item := range input.Items {
		if item.Disabled && !disabledStarted {
			rows = append(rows, Row{button("── "+copy.Text(i18n.NodeDisabled)+" ──", ActionNoop, "")})
			disabledStarted = true
		}
		label := nodeStatusGlyph(item.Status) + " " + item.Name
		if item.Disabled {
			label = "⚫ " + item.Name
		}
		if item.Leader {
			label = "👑 " + label
			if item.PinnedMinutes > 0 {
				label += fmt.Sprintf(" · %dm", item.PinnedMinutes)
			}
		}
		action, token := statusNodeAction(mode), item.Token
		rows = append(rows, Row{button(label, action, token)})
	}
	if len(input.Items) == 0 {
		rows = append(rows, Row{button(copy.Text(i18n.NoServers), ActionNoop, "")})
	}
	backAction, backToken := input.BackAction, input.BackToken
	if backAction == "" {
		backAction = ActionMenu
	}
	rows = append(rows, Row{button(copy.Text(i18n.ButtonBack), backAction, backToken)})
	return Screen{
		Name: ScreenStatus, Text: text, Grid: rows,
		RichMarkdown: mode == StatusChoose,
	}
}

func RenderLeaderConfirmation(copy i18n.Localizer, nodeName string, token OpaqueToken) Screen {
	return Screen{
		Name: ScreenStatus, Text: copy.Format(i18n.StatusConfirmLeader, nodeName),
		Grid: Grid{
			Row{button(copy.Text(i18n.ButtonConfirm), ActionConfirmLeader, token)},
			Row{button(copy.Text(i18n.ButtonCancel), ActionOpenSetting, OpaqueToken(SettingLeaderNode))},
		},
	}
}

func RenderNodeSettings(
	copy i18n.Localizer,
	name string,
	backends string,
	status NodeStatus,
) Screen {
	return Screen{
		Name: ScreenStatus,
		Text: copy.Format(i18n.StatusNodeSettings, name, backends, nodeStatusGlyph(status)),
		Grid: Grid{Row{button(copy.Text(i18n.ButtonBack), ActionStatusMode, "settings")}},
	}
}

func statusNodeAction(mode StatusMode) Action {
	switch mode {
	case StatusSettings:
		return ActionStatusSettingsNode
	default:
		return ActionSelectNode
	}
}

func statusTable(copy i18n.Localizer, items []StatusItem, now time.Time) string {
	header := strings.TrimSpace(strings.ReplaceAll(copy.Text(i18n.StatusQuotaHeader), "\\|", "|"))
	lines := []string{header, "|---|---|---|---:|---:|---|"}
	for _, item := range items {
		name := markdownTableCell(item.Name)
		if item.Disabled {
			name = "⚫ " + name
		} else if item.Status == NodeOffline {
			name = "🔴 " + name
		}
		if item.Leader {
			name = "👑 " + name
		}
		if len(item.Quotas) == 0 {
			lines = append(lines, fmt.Sprintf("| %s | — | — | %d | — | — |",
				name, ageMinutes(now, item.ObservedAt)))
			continue
		}
		for _, quota := range item.Quotas {
			lines = append(lines, fmt.Sprintf("| %s | %s | %s | %d | %s | %s |",
				name, markdownTableCell(quota.Backend), markdownTableCell(quotaUsage(copy, quota, now)),
				quotaAgeMinutes(now, quota.CollectedAt, item.ObservedAt),
				quotaTodayRemaining(quota), quotaResetAt(quota, now)))
		}
	}
	return strings.Join(lines, "\n")
}

func quotaTodayRemaining(snapshot domain.QuotaSnapshot) string {
	if snapshot.TodayRemaining == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *snapshot.TodayRemaining)
}

func quotaResetAt(snapshot domain.QuotaSnapshot, now time.Time) string {
	if snapshot.Weekly == nil || snapshot.Weekly.ResetsAt.IsZero() {
		return "—"
	}
	return snapshot.Weekly.ResetsAt.In(now.Location()).Format("02.01 15:04")
}

func stripMarkdownTable(value string) string {
	return strings.TrimSpace(strings.Trim(strings.ReplaceAll(value, "\\|", "|"), "|"))
}

func quotaAgeMinutes(now, collectedAt, observedAt time.Time) int {
	if collectedAt.IsZero() {
		collectedAt = observedAt
	}
	return ageMinutes(now, collectedAt)
}

func ageMinutes(now, observedAt time.Time) int {
	if observedAt.IsZero() || observedAt.After(now) {
		return 0
	}
	return int(now.Sub(observedAt) / time.Minute)
}

func quotaPercent(window *domain.QuotaWindow) string {
	if window == nil {
		return "—"
	}
	return fmt.Sprintf("%d%%", window.UsedPercent)
}

func quotaUsage(copy i18n.Localizer, snapshot domain.QuotaSnapshot, now time.Time) string {
	parts := make([]string, 0, 2)
	if snapshot.FiveHour != nil {
		parts = append(parts, copy.Text(i18n.QuotaWindowFiveHour)+" "+quotaPercent(snapshot.FiveHour))
	} else if budget, ok := calculatedFiveHourBudget(snapshot.Weekly, now); ok {
		parts = append(parts, fmt.Sprintf(
			"%s %.1f%%", copy.Text(i18n.QuotaWindowFiveHourBudget), budget,
		))
	}
	if snapshot.Weekly != nil {
		parts = append(parts, copy.Text(i18n.QuotaWindowWeek)+" "+quotaPercent(snapshot.Weekly))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

func calculatedFiveHourBudget(weekly *domain.QuotaWindow, now time.Time) (float64, bool) {
	if weekly == nil || weekly.ResetsAt.IsZero() || !weekly.ResetsAt.After(now) {
		return 0, false
	}
	hoursLeft := weekly.ResetsAt.Sub(now).Hours()
	budgetHours := min(5.0, hoursLeft)
	return float64(100-weekly.UsedPercent) * budgetHours / hoursLeft, true
}

func markdownTableCell(value string) string {
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "\r", " ", "\n", " ").Replace(value)
}
