package telegramstatus_test

import (
	"strings"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/telegramstatus"
)

func TestRenderMatchesLegacyQuotaTableContract(t *testing.T) {
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.FixedZone("UTC+3", 3*60*60))
	today := -4.0
	got := telegramstatus.Render(now, []telegramstatus.Node{{ID: "local", Name: "Coordinator", Coordinator: true, Available: true}}, []telegramstatus.Snapshot{{
		ComputerID: "local", Provider: domain.ProviderCodex, CollectedAt: now.Add(-2 * time.Minute),
		FiveHour: &telegramstatus.Window{UsedPercent: 12}, Weekly: &telegramstatus.Window{UsedPercent: 50, ResetsAt: time.Date(2026, 9, 6, 9, 30, 0, 0, time.UTC)}, TodayRemaining: &today,
	}})
	for _, want := range []string{
		"| Сервер | Бэк | Израсх. | Остаток | Обновлено | Сброс |\n|---|---|---|---:|---|---|",
		"| 👑 Coordinator | codex | w 50% | -4.0% · 5ч 88% | 2 | 06.09 12:30 |",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status = %q, want fragment %q", got, want)
		}
	}
}
