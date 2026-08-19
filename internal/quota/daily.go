package quota

import "time"

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
		previous = dailyState{
			Date: date, ResetsAt: resetsAt, DayStartUsed: used,
		}
	}
	// Recompute even for restored state so snapshots created by the former
	// whole-calendar-day allocation migrate immediately to hourly allocation.
	previous.Budget = hourlyDailyBudget(previous.DayStartUsed, resetsAt, now)
	spent := max(0, used-previous.DayStartUsed)
	remaining := previous.Budget - float64(spent)
	return remaining, previous, true
}

func hourlyDailyBudget(dayStartUsed int, resetsAt, now time.Time) float64 {
	resetLocal := resetsAt.In(now.Location())
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	tomorrowStart := todayStart.AddDate(0, 0, 1)
	todayEnd := minTime(tomorrowStart, resetLocal)
	windowHours := resetLocal.Sub(todayStart).Hours()
	todayHours := todayEnd.Sub(todayStart).Hours()
	return float64(max(0, 100-dayStartUsed)) * todayHours / windowHours
}

func minTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
