package nativerender

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestNativeImageProfilesTransformOnlyThePNG(t *testing.T) {
	input := "\x1b[38;2;255;100;20mcolored terminal text\x1b[0m\n" +
		"\x1b[48;2;20;80;160msecond line remains selected\x1b[0m"
	legacy, err := RenderNative(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	current, err := RenderNativeWithOptions(t.Context(), input, NativeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	explicitCurrent, err := RenderNativeWithOptions(t.Context(), input, NativeOptions{ImageProfile: ImageProfileCurrent})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(legacy, current) || !bytes.Equal(current, explicitCurrent) {
		t.Fatal("legacy, zero-options, and explicit current PNGs differ")
	}
	currentImage := decodeNativeTestPNG(t, current)

	full8, err := RenderNativeWithOptions(t.Context(), input, NativeOptions{ImageProfile: ImageProfileFull8})
	if err != nil {
		t.Fatal(err)
	}
	full8Image := decodeNativeTestPNG(t, full8)
	if full8Image.Bounds().Dx() != currentImage.Bounds().Dx() || full8Image.Bounds().Dy() != currentImage.Bounds().Dy() {
		t.Fatalf("full_8 dimensions = %v, current = %v", full8Image.Bounds(), currentImage.Bounds())
	}
	if colors := nativeTestUsedColors(full8Image); colors > 8 {
		t.Fatalf("full_8 used colors = %d, want <= 8", colors)
	}
	if bytes.Equal(full8, current) {
		t.Fatal("full_8 PNG equals the current full-palette PNG")
	}

	compact8, err := RenderNativeWithOptions(t.Context(), input, NativeOptions{ImageProfile: ImageProfileCompact8})
	if err != nil {
		t.Fatal(err)
	}
	compact8Image := decodeNativeTestPNG(t, compact8)
	wantWidth := currentImage.Bounds().Dx() * 3 / 4
	wantHeight := currentImage.Bounds().Dy() * 3 / 4
	if compact8Image.Bounds().Dx() != wantWidth || compact8Image.Bounds().Dy() != wantHeight {
		t.Fatalf("compact_8 dimensions = %dx%d, want %dx%d", compact8Image.Bounds().Dx(), compact8Image.Bounds().Dy(), wantWidth, wantHeight)
	}
	if colors := nativeTestUsedColors(compact8Image); colors > 8 {
		t.Fatalf("compact_8 used colors = %d, want <= 8", colors)
	}
	if bytes.Equal(compact8, full8) {
		t.Fatal("compact_8 PNG equals the full-size PNG")
	}
}

func TestNativeImageProfileRejectsUnknownValue(t *testing.T) {
	_, err := RenderNativeWithOptions(t.Context(), "terminal", NativeOptions{ImageProfile: ImageProfile(255)})
	if err != ErrInvalidNativeOptions {
		t.Fatalf("error = %v, want %v", err, ErrInvalidNativeOptions)
	}
}

func TestNativeImageProfilesAreByteDeterministic(t *testing.T) {
	input := strings.Repeat("\x1b[38;2;230;120;40mcolored\x1b[0m plain ", 12) + "\n" +
		strings.Repeat("\x1b[48;5;25mbackground\x1b[0m ", 12)
	for _, profile := range []ImageProfile{ImageProfileFull8, ImageProfileCompact8} {
		first, err := RenderNativeWithOptions(t.Context(), input, NativeOptions{ImageProfile: profile})
		if err != nil {
			t.Fatal(err)
		}
		for iteration := 0; iteration < 8; iteration++ {
			got, renderErr := RenderNativeWithOptions(t.Context(), input, NativeOptions{ImageProfile: profile})
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			if !bytes.Equal(got, first) {
				t.Fatalf("profile %d changed bytes on iteration %d", profile, iteration+1)
			}
		}
	}
}

func BenchmarkNativeImageProfile1472x942(b *testing.B) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1472, 942))
	for y := 0; y < canvas.Bounds().Dy(); y++ {
		for x := 0; x < canvas.Bounds().Dx(); x++ {
			pixel := color.RGBA{30, 30, 30, 255}
			if y%22 >= 4 && y%22 < 18 && x%10 < 7 {
				shade := uint8(150 + (x+y)%106)
				pixel = color.RGBA{shade, shade, shade, 255}
				if y/22%7 == 0 {
					pixel = color.RGBA{20, shade, 220, 255}
				}
			}
			canvas.SetRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		b.Fatal(err)
	}
	source := encoded.Bytes()
	for _, benchmark := range []struct {
		name    string
		profile ImageProfile
	}{
		{"full_8", ImageProfileFull8},
		{"compact_8", ImageProfileCompact8},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(source)))
			for range b.N {
				if _, err := applyNativeImageProfile(source, benchmark.profile); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func decodeNativeTestPNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func nativeTestUsedColors(img image.Image) int {
	used := make(map[color.Color]struct{})
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			used[color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}] = struct{}{}
		}
	}
	return len(used)
}

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
