package telegramui

import (
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/i18n"
)

type BackgroundItem struct {
	Name           string
	NodeName       string
	Marker         string
	Status         string
	ContextPercent *int
}

func RenderBackgroundPanel(
	copy i18n.Localizer,
	items []BackgroundItem,
	extra int,
) string {
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items)+2)
	lines = append(lines, copy.Text(i18n.BackgroundPanelTitle))
	for _, item := range items {
		parts := []string{item.Marker, item.Name}
		if item.NodeName != "" {
			parts = append(parts, "·", item.NodeName)
		}
		parts = append(parts, item.Status)
		line := strings.Join(nonEmpty(parts), " ")
		if item.ContextPercent != nil {
			line += fmt.Sprintf(" · %d%%", *item.ContextPercent)
		}
		lines = append(lines, line)
	}
	if extra > 0 {
		lines = append(lines, copy.Format(i18n.BackgroundPanelMore, extra))
	}
	return strings.Join(lines, "  \n")
}

func nonEmpty(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func BackgroundStatusGlyph(status string) string {
	switch status {
	case "working":
		return "⏳"
	case "finished":
		return "✅"
	case "error":
		return "❌"
	case "needs_action":
		return "❓"
	default:
		return fmt.Sprintf("[%s]", status)
	}
}
