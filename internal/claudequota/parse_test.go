package claudequota

import (
	"testing"
	"time"
)

func TestParseUsage(t *testing.T) {
	now := time.Date(2026, time.September, 5, 10, 0, 0, 0, time.Local)
	input := "Current session\n18% used\nResets 1:00 pm\nCurrent week (all models)\n42% used\nResets Sep 8 at 2:30 pm"
	snapshot, ok := Parse(input, "local", now)
	if !ok || snapshot.FiveHour == nil || snapshot.FiveHour.UsedPercent != 18 ||
		snapshot.Weekly == nil || snapshot.Weekly.UsedPercent != 42 || snapshot.Weekly.ResetsAt.IsZero() {
		t.Fatalf("snapshot=%#v ok=%v", snapshot, ok)
	}
}
