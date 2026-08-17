package quota

import (
	"testing"
	"time"
)

func TestDailyRemainderKeepsBaselineWithinDay(t *testing.T) {
	now := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	remaining, state, ok := dailyRemainder(40, reset, dailyState{}, now)
	if !ok || remaining != 20 {
		t.Fatalf("initial remaining=%v ok=%v", remaining, ok)
	}
	remaining, state, ok = dailyRemainder(47, reset, state, now.Add(2*time.Hour))
	if !ok || remaining != 13 || state.DayStartUsed != 40 {
		t.Fatalf("later remaining=%v state=%#v", remaining, state)
	}
	remaining, state, ok = dailyRemainder(48, reset, state, now.Add(24*time.Hour))
	if !ok || remaining != 26 || state.DayStartUsed != 48 {
		t.Fatalf("next-day remaining=%v state=%#v", remaining, state)
	}
}

func TestDailyRemainderMatchesCCBotCalendarBucketsAndOverspend(t *testing.T) {
	now := time.Date(2026, 7, 31, 18, 0, 0, 0, time.Local)
	reset := now.Add(3*24*time.Hour + 6*time.Hour)
	remaining, state, ok := dailyRemainder(50, reset, dailyState{}, now)
	if !ok || remaining != 10 || state.Budget != 10 {
		t.Fatalf("initial remaining=%v state=%#v ok=%v", remaining, state, ok)
	}
	remaining, _, ok = dailyRemainder(64, reset, state, now.Add(time.Hour))
	if !ok || remaining != -4 {
		t.Fatalf("overspend remaining=%v ok=%v", remaining, ok)
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
	if !ok || remaining != 9 || state.Budget != 9 || state.DayStartUsed != 64 {
		t.Fatalf("next-day remaining=%v state=%#v ok=%v", remaining, state, ok)
	}
}
