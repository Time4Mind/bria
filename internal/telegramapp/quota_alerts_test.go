package telegramapp

import (
	"testing"
	"time"
)

func TestQuotaAlertLevelsAndWindowIdentity(t *testing.T) {
	for percent, want := range map[int]int{0: 0, 49: 0, 50: 1, 74: 1, 75: 2, 89: 2, 90: 3, 100: 3} {
		if got := quotaAlertLevel(percent); got != want {
			t.Fatalf("level(%d)=%d want=%d", percent, got, want)
		}
	}
	reset := time.Unix(100, 0)
	if !sameQuotaWindow(reset, reset) || !sameQuotaWindow(time.Time{}, reset) ||
		sameQuotaWindow(reset, reset.Add(time.Second)) {
		t.Fatal("quota window comparison is inconsistent")
	}
}
