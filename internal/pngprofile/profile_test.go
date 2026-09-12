package pngprofile_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"bria/internal/pngprofile"
)

func TestProfilesPreserveCurrentAndBoundEightColorGeometry(t *testing.T) {
	source := testPNG(t)
	current, err := pngprofile.Apply(source, pngprofile.ProfileCurrent)
	if err != nil || !bytes.Equal(current, source) {
		t.Fatalf("current profile changed source PNG: %v", err)
	}

	for _, test := range []struct {
		profile       pngprofile.Profile
		width, height int
	}{
		{pngprofile.ProfileFull8, 40, 20},
		{pngprofile.ProfileCompact8, 30, 15},
	} {
		first, err := pngprofile.Apply(source, test.profile)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := png.Decode(bytes.NewReader(first))
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Bounds().Dx() != test.width || decoded.Bounds().Dy() != test.height {
			t.Fatalf("profile %d dimensions = %v", test.profile, decoded.Bounds())
		}
		if usedColors(decoded) > 8 {
			t.Fatalf("profile %d exceeds eight used colors", test.profile)
		}
		second, err := pngprofile.Apply(source, test.profile)
		if err != nil || !bytes.Equal(second, first) {
			t.Fatalf("profile %d is not deterministic: %v", test.profile, err)
		}
	}
}

func TestProfileRejectsUnknownValue(t *testing.T) {
	if _, err := pngprofile.Apply(testPNG(t), pngprofile.Profile(255)); !errors.Is(err, pngprofile.ErrInvalidProfile) {
		t.Fatalf("error = %v", err)
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 12), B: uint8((x + y) * 4), A: 255})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func usedColors(source image.Image) int {
	colors := make(map[color.RGBA64]struct{})
	for y := source.Bounds().Min.Y; y < source.Bounds().Max.Y; y++ {
		for x := source.Bounds().Min.X; x < source.Bounds().Max.X; x++ {
			r, g, b, a := source.At(x, y).RGBA()
			colors[color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}] = struct{}{}
		}
	}
	return len(colors)
}
