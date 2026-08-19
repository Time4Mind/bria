package quota

import (
	"math"
	"testing"
	"time"
)

func TestDailyRemainderKeepsBaselineWithinDay(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	remaining, state, ok := dailyRemainder(40, reset, dailyState{}, now)
	wantBudget := 60.0 * 24 / 58
	if !ok || !closeFloat(remaining, wantBudget) {
		t.Fatalf("initial remaining=%v ok=%v", remaining, ok)
	}
	remaining, state, ok = dailyRemainder(47, reset, state, now.Add(2*time.Hour))
	if !ok || !closeFloat(remaining, wantBudget-7) || state.DayStartUsed != 40 {
		t.Fatalf("later remaining=%v state=%#v", remaining, state)
	}
	remaining, state, ok = dailyRemainder(48, reset, state, now.Add(24*time.Hour))
	wantNextBudget := 52.0 * 24 / 34
	if !ok || !closeFloat(remaining, wantNextBudget) || state.DayStartUsed != 48 {
		t.Fatalf("next-day remaining=%v state=%#v", remaining, state)
	}
}

func TestDailyRemainderUsesExactResetHoursAndAllowsOverspend(t *testing.T) {
	now := time.Date(2026, 7, 31, 18, 0, 0, 0, time.Local)
	reset := now.Add(3*24*time.Hour + 6*time.Hour)
	remaining, state, ok := dailyRemainder(50, reset, dailyState{}, now)
	wantBudget := 50.0 * 24 / 96
	if !ok || !closeFloat(remaining, wantBudget) || !closeFloat(state.Budget, wantBudget) {
		t.Fatalf("initial remaining=%v state=%#v ok=%v", remaining, state, ok)
	}
	remaining, _, ok = dailyRemainder(64, reset, state, now.Add(time.Hour))
	if !ok || !closeFloat(remaining, wantBudget-14) {
		t.Fatalf("overspend remaining=%v ok=%v", remaining, ok)
	}
}

func TestDailyRemainderProratesPartialResetDayByHour(t *testing.T) {
	location := time.FixedZone("UTC+3", 3*60*60)
	now := time.Date(2026, 8, 19, 16, 0, 0, 0, location)
	reset := time.Date(2026, 8, 20, 7, 27, 0, 0, location)
	remaining, state, ok := dailyRemainder(72, reset, dailyState{}, now)
	wantBudget := 28.0 * 24 / (31.0 + 27.0/60)
	if !ok || !closeFloat(remaining, wantBudget) || !closeFloat(state.Budget, wantBudget) {
		t.Fatalf("initial remaining=%v state=%#v ok=%v", remaining, state, ok)
	}
	remaining, _, ok = dailyRemainder(83, reset, state, now.Add(time.Hour))
	if !ok || !closeFloat(remaining, wantBudget-11) {
		t.Fatalf("later remaining=%v want=%v ok=%v", remaining, wantBudget-11, ok)
	}
}

func TestDailyRemainderRedistributesOnlyOnNextLocalDay(t *testing.T) {
	location := time.FixedZone("UTC+3", 3*60*60)
	now := time.Date(2026, 7, 31, 18, 0, 0, 0, location)
	reset := time.Date(2026, 8, 4, 20, 0, 0, 0, location)
	_, state, ok := dailyRemainder(50, reset, dailyState{}, now)
	if !ok {
		t.Fatal("initial daily budget unavailable")
	}
	remaining, state, ok := dailyRemainder(
		64, reset, state, time.Date(2026, 8, 1, 9, 0, 0, 0, location),
	)
	wantBudget := 36.0 * 24 / 92
	if !ok || !closeFloat(remaining, wantBudget) || !closeFloat(state.Budget, wantBudget) ||
		state.DayStartUsed != 64 {
		t.Fatalf("next-day remaining=%v state=%#v ok=%v", remaining, state, ok)
	}
}

func closeFloat(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
