package telegramui

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

func TestClusterUpdateCardUsesOneCompactProgressScreen(t *testing.T) {
	update := domain.ClusterUpdate{
		Version: "v2", Phase: domain.ClusterUpdateRunning,
		Order: []domain.NodeID{"a", "b"},
		Nodes: map[domain.NodeID]domain.NodeUpdate{
			"a": {Phase: domain.NodeUpdateHealthy},
			"b": {Phase: domain.NodeUpdateInstalling},
		},
	}
	screen := RenderClusterUpdate(i18n.For("ru"), update, map[domain.NodeID]domain.Node{
		"a": {ID: "a", Name: "Первый"}, "b": {ID: "b", Name: "Второй"},
	})
	if !strings.Contains(screen.Text, "✅ Первый") || !strings.Contains(screen.Text, "⬇️ Второй") {
		t.Fatalf("progress is not compact or complete: %q", screen.Text)
	}
	assertGoldenGrid(t, screen, `[🔄 Обновить -> update_refresh]
[← Назад -> settings_cat@cluster]`)
}

func TestFailedClusterUpdateOffersRetryFromFailedNode(t *testing.T) {
	update := domain.ClusterUpdate{
		Version: "v2", Phase: domain.ClusterUpdateFailed, Error: "boom",
		Order: []domain.NodeID{"a"},
		Nodes: map[domain.NodeID]domain.NodeUpdate{
			"a": {Phase: domain.NodeUpdateFailed, Error: "boom"},
		},
	}
	screen := RenderClusterUpdate(i18n.For("ru"), update, map[domain.NodeID]domain.Node{
		"a": {ID: "a", Name: "Первая", Version: "v1"},
	})
	if !strings.Contains(screen.Text, "ошибка") || strings.Contains(screen.Text, "%!s") {
		t.Fatalf("failed phase is malformed: %q", screen.Text)
	}
	assertGoldenGrid(t, screen, `[↻ Повторить с упавшей ноды -> update_retry]
[← Назад -> settings_cat@cluster]`)
}
