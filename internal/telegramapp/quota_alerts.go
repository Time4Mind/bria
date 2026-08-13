package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type quotaAlertCheckpoint struct {
	level    int
	resetsAt time.Time
}

func (h *Handler) RunQuotaNotifications(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	checkpoints := make(map[string]quotaAlertCheckpoint)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if h.canRefresh() {
			h.scanQuotaNotifications(ctx, checkpoints)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) scanQuotaNotifications(
	ctx context.Context,
	checkpoints map[string]quotaAlertCheckpoint,
) {
	for _, observation := range h.service.QuotaAlertObservations() {
		level := quotaAlertLevel(observation.UsedPercent)
		previous, exists := checkpoints[observation.Key]
		current := quotaAlertCheckpoint{level: level, resetsAt: observation.ResetsAt}
		checkpoints[observation.Key] = current
		if !exists || !sameQuotaWindow(previous.resetsAt, current.resetsAt) ||
			level <= previous.level {
			continue
		}
		h.sendQuotaNotification(ctx, observation)
	}
}

func (h *Handler) sendQuotaNotification(
	ctx context.Context,
	observation application.QuotaAlertObservation,
) {
	copy := h.copy(application.Principal{UserID: observation.UserID})
	window := copy.Text(i18n.QuotaWindowFiveHour)
	if observation.Window == "week" {
		window = copy.Text(i18n.QuotaWindowWeek)
	}
	glyph := "🟡"
	if observation.UsedPercent >= 90 {
		glyph = "🔴"
	} else if observation.UsedPercent >= 75 {
		glyph = "🟠"
	}
	_, _ = h.messenger.SendScreen(ctx, int64(observation.UserID), telegramui.Screen{
		Name: telegramui.ScreenStatus,
		Text: copy.Format(i18n.QuotaAlert, glyph, observation.Backend,
			observation.AccountLabel, window, observation.UsedPercent),
	})
}

func quotaAlertLevel(percent int) int {
	switch {
	case percent >= 90:
		return 3
	case percent >= 75:
		return 2
	case percent >= 50:
		return 1
	default:
		return 0
	}
}

func sameQuotaWindow(left, right time.Time) bool {
	return left.Equal(right) || left.IsZero() || right.IsZero()
}
