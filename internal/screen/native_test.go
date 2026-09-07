package screen_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"bria/internal/screen"
)

func TestNativeRenderPreservesCyrillicAndBounds(t *testing.T) {
	actual, err := screen.RenderNative(context.Background(), "Привет")
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := screen.RenderNative(context.Background(), "??????")
	if err != nil || bytes.Equal(actual, fallback) {
		t.Fatal("Cyrillic rendered as missing glyphs")
	}
	if _, err := screen.RenderNativeWithOptions(context.Background(), "screen", screen.NativeOptions{CaptureKiB: 49}); !errors.Is(err, screen.ErrInvalidNativeOptions) {
		t.Fatalf("unsupported capture limit error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := screen.RenderNative(ctx, "screen"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNativeCaptureLimitVariantsKeepNewestUsefulPayload(t *testing.T) {
	for _, captureKiB := range []int{48, 64, 86} {
		t.Run(fmt.Sprint(captureKiB), func(t *testing.T) {
			old := strings.Repeat("old\n", (captureKiB<<10)/4)
			data, err := screen.RenderNativeWithOptions(context.Background(), old+"newest", screen.NativeOptions{CaptureKiB: captureKiB})
			if err != nil {
				t.Fatal(err)
			}
			if len(data) == 0 || len(data) > screen.NativeMaxPNGBytes {
				t.Fatalf("PNG size = %d", len(data))
			}
		})
	}
}

func TestNativeRenderPreservesInteriorBlankRowsWithoutArtificialWidth(t *testing.T) {
	data, err := screen.RenderNative(context.Background(), "x\n\n\nx")
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if config.Width > 64 {
		t.Fatalf("single-column capture got artificial background width %d", config.Width)
	}
	if config.Height < 100 {
		t.Fatalf("interior blank rows were discarded: height %d", config.Height)
	}
}

func TestNativeRenderTrimsOldestRowsToPNGByteLimit(t *testing.T) {
	var input strings.Builder
	for row := 0; row < 86; row++ {
		for column := 0; column < 358; column++ {
			fmt.Fprintf(&input, "\x1b[38;2;%d;%d;%dm%c", (row*17+column*7)%256, (row*31+column*11)%256, (row*47+column*13)%256, 'A'+rune((row+column)%26))
		}
		input.WriteByte('\n')
	}
	data, err := screen.RenderNativeWithOptions(context.Background(), input.String(), screen.NativeOptions{CaptureKiB: 86})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > screen.NativeMaxPNGBytes {
		t.Fatalf("PNG size = %d, limit = %d", len(data), screen.NativeMaxPNGBytes)
	}
}

func TestNativeRenderPreservesANSIColourOnDarkBackground(t *testing.T) {
	data, err := screen.RenderNative(context.Background(), "\x1b[31merror\x1b[0m")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	foundRed := false
	for y := decoded.Bounds().Min.Y; y < decoded.Bounds().Max.Y && !foundRed; y++ {
		for x := decoded.Bounds().Min.X; x < decoded.Bounds().Max.X; x++ {
			r, g, b, _ := decoded.At(x, y).RGBA()
			if r > g*2 && r > b*2 && r > 0x4000 {
				foundRed = true
				break
			}
		}
	}
	if !foundRed {
		t.Fatal("ANSI foreground colour was discarded")
	}
}

func TestLargestNativeRenderFitsTelegramPhotoDimensionLimit(t *testing.T) {
	// The default 48 KiB profile uses the historical 200x48 useful-payload
	// viewport, 20px Go Mono and 16px padding.
	for _, text := range []string{strings.Repeat("x", 240), strings.Repeat("x\n", 200), strings.Repeat(strings.Repeat("x", 200)+"\n", 48)} {
		data, err := screen.RenderNative(context.Background(), text)
		if err != nil {
			t.Fatal(err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if config.Width > 3200 || config.Height > 1800 {
			t.Fatalf("native PNG exceeds upload bound: %dx%d", config.Width, config.Height)
		}
	}
}
