package telegramui

import (
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

func TestStatusRendersQuotaTableAndRefreshBeforeModes(t *testing.T) {
	now := time.Unix(600, 0).UTC()
	todayRemaining := -4.0
	dailyBudget := domain.QuotaDailyBudget{Budget: 10}
	weeklyReset := time.Date(2026, 8, 20, 12, 30, 0, 0, time.UTC)
	screen := RenderStatus(StatusInput{
		Copy: englishCopy, Mode: StatusChoose, Now: now,
		Items: []StatusItem{
			{
				Token: "laptop", Name: "Laptop", Status: NodeOnline, Leader: true,
				Quotas: []domain.QuotaSnapshot{{
					NodeID: "laptop", Backend: "codex", CollectedAt: now.Add(-2 * time.Minute),
					FiveHour:       &domain.QuotaWindow{UsedPercent: 12},
					Weekly:         &domain.QuotaWindow{UsedPercent: 50, ResetsAt: weeklyReset},
					TodayRemaining: &todayRemaining,
					DailyBudget:    &dailyBudget,
				}},
			},
			{Token: "game", Name: "Game", Status: NodeOffline, ObservedAt: now.Add(-9 * time.Minute)},
		},
	})
	assertGoldenGrid(t, screen, `[🔄 Refresh -> status_refresh@choose]
[• Select -> status_mode@choose] | [Settings -> status_mode@settings]
[👑 🟢 Laptop -> node@laptop]
[🔴 Game -> node@game]
[← Back -> menu]`)
	for _, value := range []string{
		"| Server | Back | Used | Age, min | Today left | Reset |\n|---|---|---|---:|---:|---|",
		"| 👑 Laptop | codex | 5h 12% · week 50% | 2 | -4.0% | 20.08 12:30 |",
		"| 🔴 Game | — | — | 9 | — | — |",
	} {
		if !strings.Contains(screen.Text, value) {
			t.Fatalf("status text %q does not contain %q", screen.Text, value)
		}
	}
	if !screen.RichMarkdown || screen.ParseMode != "" {
		t.Fatalf("status table must use the rich Markdown renderer: %+v", screen)
	}
}

func TestStatusTableHeaderIsLocalized(t *testing.T) {
	screen := RenderStatus(StatusInput{Copy: i18n.For("ru"), Mode: StatusChoose, Now: time.Now()})
	if !strings.Contains(screen.Text, "Сервер | Бэк | Израсх. | Возраст, мин | Сегодня остаток | Сброс") {
		t.Fatalf("status header=%q", screen.Text)
	}
}

func TestStatusResetUsesInterfaceLocalTimezone(t *testing.T) {
	location := time.FixedZone("UTC+3", 3*60*60)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, location)
	remaining := 8.25
	dailyBudget := domain.QuotaDailyBudget{Budget: 10}
	screen := RenderStatus(StatusInput{
		Copy: englishCopy, Mode: StatusChoose, Now: now,
		Items: []StatusItem{{Name: "Laptop", Status: NodeOnline, Quotas: []domain.QuotaSnapshot{{
			NodeID: "laptop", Backend: "codex", TodayRemaining: &remaining,
			DailyBudget: &dailyBudget,
			Weekly:      &domain.QuotaWindow{UsedPercent: 50, ResetsAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
		}}}},
	})
	if !strings.Contains(screen.Text, "| Laptop | codex | 5h 3.4% · week 50% | 0 | 8.2% | 20.08 13:00 |") {
		t.Fatalf("status text=%q", screen.Text)
	}
}

func TestStatusRendersCalculatedFiveHourBudget(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	screen := RenderStatus(StatusInput{
		Copy: englishCopy, Mode: StatusChoose, Now: now,
		Items: []StatusItem{{Name: "Laptop", Status: NodeOnline, Quotas: []domain.QuotaSnapshot{{
			NodeID: "laptop", Backend: "codex",
			Weekly: &domain.QuotaWindow{UsedPercent: 85, ResetsAt: now.Add(20 * time.Hour)},
		}}}},
	})
	if !strings.Contains(screen.Text, "5h 3.8% · week 85%") {
		t.Fatalf("status text=%q", screen.Text)
	}
}
