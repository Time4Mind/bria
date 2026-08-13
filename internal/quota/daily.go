package quota

import (
	"math"
	"time"
)

type dailyState struct {
	Date         string
	ResetsAt     time.Time
	DayStartUsed int
	Budget       float64
}

func dailyRemainder(
	used int,
	resetsAt time.Time,
	previous dailyState,
	now time.Time,
) (float64, dailyState, bool) {
	if resetsAt.IsZero() || !resetsAt.After(now) {
		return 0, dailyState{}, false
	}
	date := now.Format("2006-01-02")
	sameWindow := previous.ResetsAt.Equal(resetsAt)
	sameDay := previous.Date == date
	if !sameWindow || !sameDay || used < previous.DayStartUsed {
		resetLocal := resetsAt.In(now.Location())
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		resetStart := time.Date(
			resetLocal.Year(), resetLocal.Month(), resetLocal.Day(), 0, 0, 0, 0, now.Location(),
		)
		days := 1
		for day := todayStart; day.Before(resetStart); days++ {
			day = day.AddDate(0, 0, 1)
		}
		previous = dailyState{
			Date: date, ResetsAt: resetsAt, DayStartUsed: used,
			Budget: float64(max(0, 100-used)) / float64(days),
		}
	}
	spent := max(0, used-previous.DayStartUsed)
	remaining := max(0, previous.Budget-float64(spent))
	return math.Round(remaining*10) / 10, previous, true
}
