package telegramui

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/i18n"
)

func TestClusterHealthRendersEvidenceAndAgentHandoff(t *testing.T) {
	screen := RenderClusterHealth(ClusterHealthInput{
		Copy: i18n.For("ru"), Leader: "alpha", Online: 1, Enabled: 2,
		CacheEntries: 12, CacheLimit: 64, CacheEvictions: 3,
		TranscriptAverage: "25ms", TranscriptMaximum: "3s", TranscriptTimeouts: 1,
		Findings: []ClusterHealthFinding{{
			Severity: "warning", Title: "Медленное чтение transcript", Evidence: "max=3s",
		}},
		AgentAvailable: true,
	})
	if !strings.Contains(screen.Text, "Ноды: 1/2") ||
		!strings.Contains(screen.Text, "Медленное чтение transcript") {
		t.Fatalf("health screen=%q", screen.Text)
	}
	assertGoldenGrid(t, screen, `[🔄 Обновить -> health_refresh]
[Исправить с агентом -> health_agent]
[← Назад -> settings_cat@cluster]`)
}
