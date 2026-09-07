// Package telegramstatus renders the read-only provider quota summary shown
// above the existing node controls on Telegram's Status screen.
package telegramstatus

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"time"

	"bria/internal/domain"
)

type Window struct {
	UsedPercent int
	ResetsAt    time.Time
}

type Snapshot struct {
	ComputerID     domain.ComputerID
	Provider       domain.Provider
	FiveHour       *Window
	Weekly         *Window
	TodayRemaining *float64
	CollectedAt    time.Time
}

type Reader interface {
	Snapshots(context.Context) ([]Snapshot, error)
}

type Node struct {
	ID          domain.ComputerID
	Name        string
	Coordinator bool
	Available   bool
	ObservedAt  time.Time
}

func Render(now time.Time, nodes []Node, snapshots []Snapshot) string {
	byNode := make(map[domain.ComputerID][]Snapshot)
	for _, snapshot := range snapshots {
		byNode[snapshot.ComputerID] = append(byNode[snapshot.ComputerID], snapshot)
	}
	lines := []string{
		"Ноды",
		"",
		"\u00a0",
		"",
		"| Сервер | Бэк | Израсх. | Остаток | Обновлено | Сброс |",
		"|---|---|---|---:|---|---|",
	}
	for _, node := range nodes {
		quotas := byNode[node.ID]
		sort.SliceStable(quotas, func(i, j int) bool { return quotas[i].Provider < quotas[j].Provider })
		name := cell(node.Name)
		if node.Coordinator {
			name = "👑 " + name
		}
		if !node.Available {
			name = "🔴 " + name
		}
		if len(quotas) == 0 {
			updated := age(now, node.ObservedAt)
			if !node.Available && !node.ObservedAt.IsZero() && now.Sub(node.ObservedAt) > 72*time.Hour {
				updated = "—"
			}
			lines = append(lines, fmt.Sprintf("| %s | — | — | — | %s | — |", name, updated))
			continue
		}
		for _, quota := range quotas {
			if !node.Available && !node.ObservedAt.IsZero() && now.Sub(node.ObservedAt) > 72*time.Hour {
				lines = append(lines, fmt.Sprintf("| %s | %s | — | — | — | — |", name, cell(string(quota.Provider))))
				continue
			}
			lines = append(lines, fmt.Sprintf("| %s | %s | %s | %s | %s | %s |",
				name, cell(string(quota.Provider)), usage(quota), remaining(quota),
				age(now, firstTime(quota.CollectedAt, node.ObservedAt)), reset(quota, now)))
		}
	}
	return strings.Join(lines, "\n")
}

func usage(snapshot Snapshot) string {
	if snapshot.Weekly == nil {
		return "—"
	}
	return fmt.Sprintf("w %d%%", snapshot.Weekly.UsedPercent)
}

func remaining(snapshot Snapshot) string {
	parts := make([]string, 0, 2)
	if snapshot.TodayRemaining != nil {
		parts = append(parts, fmt.Sprintf("%.1f%%", *snapshot.TodayRemaining))
	}
	if snapshot.FiveHour != nil {
		parts = append(parts, fmt.Sprintf("5ч %d%%", 100-snapshot.FiveHour.UsedPercent))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

func reset(snapshot Snapshot, now time.Time) string {
	if snapshot.Weekly == nil || snapshot.Weekly.ResetsAt.IsZero() {
		return "—"
	}
	return snapshot.Weekly.ResetsAt.In(now.Location()).Format("02.01 15:04")
}

func age(now, observed time.Time) string {
	if observed.IsZero() || observed.After(now) {
		return "0"
	}
	return fmt.Sprintf("%d", int(now.Sub(observed)/time.Minute))
}

func firstTime(primary, fallback time.Time) time.Time {
	if primary.IsZero() {
		return fallback
	}
	return primary
}

func cell(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "`", "\\`", "*", "\\*", "_", "\\_",
		"~", "\\~", "[", "\\[", "]", "\\]").Replace(html.EscapeString(value))
}
