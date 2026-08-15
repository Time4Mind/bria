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
	screen := RenderStatus(StatusInput{
		Copy: englishCopy, Mode: StatusChoose, Now: now,
		Items: []StatusItem{
			{
				Token: "laptop", Name: "Laptop", Status: NodeOnline, Leader: true,
				Quotas: []domain.QuotaSnapshot{{
					NodeID: "laptop", Backend: "codex", CollectedAt: now.Add(-2 * time.Minute),
					FiveHour: &domain.QuotaWindow{UsedPercent: 12},
					Weekly:   &domain.QuotaWindow{UsedPercent: 50},
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
		"| Server | Back | Used | Age, min |\n|---|---|---|---:|",
		"| 👑 Laptop | codex | 5h 12% · week 50% | 2 |",
		"| 🔴 Game | — | — | 9 |",
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
	if !strings.Contains(screen.Text, "Сервер | Бэк | Израсх. | Возраст, мин") {
		t.Fatalf("status header=%q", screen.Text)
	}
}
