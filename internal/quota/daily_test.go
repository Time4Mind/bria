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
