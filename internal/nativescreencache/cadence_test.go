package nativescreencache

import (
	"testing"
	"time"
)

func TestScreenshotCadenceGrowsToFiveSeconds(t *testing.T) {
	tests := []struct {
		captures int
		want     time.Duration
	}{
		{0, 2500 * time.Millisecond},
		{9, 2500 * time.Millisecond},
		{10, 3500 * time.Millisecond},
		{29, 3500 * time.Millisecond},
		{30, 5 * time.Second},
	}
	for _, test := range tests {
		if got := screenshotInterval(test.captures); got != test.want {
			t.Errorf("captures=%d interval=%s want %s", test.captures, got, test.want)
		}
	}
}
