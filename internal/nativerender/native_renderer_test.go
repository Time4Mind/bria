package nativerender

import (
	"bytes"
	"image/png"
	"strings"
	"testing"
)

func TestNativeLimitsScaleWithCaptureBudget(t *testing.T) {
	tests := []struct {
		captureKiB, lines, columns int
	}{
		{48, 48, 200},
		{64, 64, 267},
		{86, 86, 359},
	}
	for _, test := range tests {
		limits, err := nativeLimitsFor(NativeOptions{CaptureKiB: test.captureKiB})
		if err != nil {
			t.Fatal(err)
		}
		if limits.captureBytes != test.captureKiB<<10 || limits.maxLines != test.lines || limits.maxColumns != test.columns {
			t.Fatalf("%d KiB limits = %+v", test.captureKiB, limits)
		}
	}
}

func TestTrimNativeCaptureKeepsNewestCompleteRows(t *testing.T) {
	got := trimNativeCapture("oldest\nolder\nnewest", len("der\nnewest"))
	if got != "newest" {
		t.Fatalf("trimmed capture = %q", got)
	}
}

func TestNativePNGByteLimitDropsOldestRows(t *testing.T) {
	limits, err := nativeLimitsFor(NativeOptions{CaptureKiB: 48})
	if err != nil {
		t.Fatal(err)
	}
	limits.maxPNGBytes = 6 << 10
	input := strings.Repeat("old row with enough text to consume pixels 0123456789\n", 47) + "newest row"
	data, _, _, err := renderNativeANSI(input, limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > limits.maxPNGBytes {
		t.Fatalf("PNG size = %d", len(data))
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	fullLimits := limits
	fullLimits.maxPNGBytes = NativeMaxPNGBytes
	full, _, fullHeight, err := renderNativeANSI(input, fullLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) <= limits.maxPNGBytes || config.Height >= fullHeight {
		t.Fatalf("old rows were not trimmed: limited=%dB/%dpx full=%dB/%dpx", len(data), config.Height, len(full), fullHeight)
	}
}
