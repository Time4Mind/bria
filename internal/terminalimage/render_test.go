package terminalimage

import (
	"bytes"
	"errors"
	"image/png"
	"strings"
	"testing"
)

func TestRenderProducesDeterministicBoundedPNG(t *testing.T) {
	options := Options{FontSize: 16, Padding: 8, MaxColumns: 12, MaxLines: 3}
	first, err := Render("\x1b[32mhello\x1b[0m\nworld", options)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := Render("\x1b[32mhello\x1b[0m\nworld", options)
	if err != nil {
		t.Fatalf("Render second time: %v", err)
	}
	if first.Hash != second.Hash || !bytes.Equal(first.PNG, second.PNG) {
		t.Fatal("embedded-font rendering is not deterministic")
	}
	decoded, err := png.Decode(bytes.NewReader(first.PNG))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if got := decoded.Bounds().Dx(); got != first.Width || got > DefaultMaxWidth {
		t.Fatalf("PNG width = %d, result width = %d", got, first.Width)
	}
	if got := decoded.Bounds().Dy(); got != first.Height || got > DefaultMaxHeight {
		t.Fatalf("PNG height = %d, result height = %d", got, first.Height)
	}
	if len(first.Hash) != 64 {
		t.Fatalf("SHA-256 hash length = %d", len(first.Hash))
	}
}

func TestRenderRejectsOversizedInputAndOutput(t *testing.T) {
	if _, err := Render(strings.Repeat("x", 5), Options{MaxInputBytes: 4}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized input error = %v", err)
	}
	if _, err := Render("visible", Options{MaxPNGBytes: 4}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized output error = %v", err)
	}
}

func TestRenderTruncatesDimensions(t *testing.T) {
	text := strings.Repeat(strings.Repeat("x", 80)+"\n", 20)
	result, err := Render(text, Options{
		FontSize: 12, MaxColumns: 10, MaxLines: 4, MaxWidth: 120, MaxHeight: 100,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Width > 120 || result.Height > 100 {
		t.Fatalf("dimensions = %dx%d", result.Width, result.Height)
	}
}
