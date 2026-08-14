// Package terminalimage renders a bounded tmux pane capture as a PNG using an
// embedded Go Mono font. It has no platform font or runtime asset dependency.
package terminalimage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"io"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	DefaultMaxInputBytes = 256 << 10
	DefaultMaxPNGBytes   = 10 << 20
	DefaultMaxColumns    = 200
	DefaultMaxLines      = 48
	DefaultMaxWidth      = 3200
	DefaultMaxHeight     = 1800
)

var (
	ErrLimitExceeded = errors.New("terminal image limit exceeded")
	ErrEmptyCapture  = errors.New("terminal image has no visible cells")
)

type Options struct {
	FontSize      float64
	Padding       int
	MaxInputBytes int
	MaxPNGBytes   int
	MaxColumns    int
	MaxLines      int
	MaxWidth      int
	MaxHeight     int
}

type Result struct {
	PNG    []byte
	Hash   string
	Width  int
	Height int
}

var (
	parseFontOnce sync.Once
	parsedFont    *opentype.Font
	parsedFontErr error
)

func Render(text string, options Options) (Result, error) {
	options = withDefaults(options)
	if len(text) > options.MaxInputBytes {
		return Result{}, ErrLimitExceeded
	}
	face, err := monoFace(options.FontSize)
	if err != nil {
		return Result{}, err
	}
	if closer, ok := face.(io.Closer); ok {
		defer closer.Close()
	}
	lines := parseANSI(text, options.MaxLines, options.MaxColumns)
	if len(lines) == 0 {
		return Result{}, ErrEmptyCapture
	}
	metrics := face.Metrics()
	cellWidth := max(1, font.MeasureString(face, "M").Ceil())
	lineHeight := max(1, metrics.Height.Round()+2)
	columns := 1
	for _, line := range lines {
		columns = max(columns, len(line))
	}
	width := min(options.MaxWidth, columns*cellWidth+2*options.Padding)
	height := min(options.MaxHeight, len(lines)*lineHeight+2*options.Padding)
	// Telegram accepts extreme aspect ratios, but its mobile client renders a
	// tall narrow image as a large near-empty strip. Keep live pane snapshots
	// at no more than 2:1 portrait while retaining the 20:1 landscape limit.
	width = min(options.MaxWidth, max(width, (height+1)/2))
	height = min(options.MaxHeight, max(height, (width+19)/20))
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(defaultBackground), image.Point{}, draw.Src)
	drawer := font.Drawer{Dst: canvas, Face: face}
	baseline := options.Padding + metrics.Ascent.Round()
	for row, line := range lines {
		y := options.Padding + row*lineHeight
		if y+lineHeight > height {
			break
		}
		for column, item := range line {
			x := options.Padding + column*cellWidth
			if x+cellWidth > width {
				break
			}
			foreground, background := item.style.colors()
			draw.Draw(canvas, image.Rect(x, y, x+cellWidth, y+lineHeight), image.NewUniform(background), image.Point{}, draw.Src)
			drawer.Src = image.NewUniform(foreground)
			drawer.Dot.X = fixedInt(x)
			drawer.Dot.Y = fixedInt(baseline + row*lineHeight)
			drawer.DrawString(string(item.value))
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return Result{}, err
	}
	if output.Len() > options.MaxPNGBytes {
		return Result{}, ErrLimitExceeded
	}
	digest := sha256.Sum256(output.Bytes())
	return Result{
		PNG: output.Bytes(), Hash: hex.EncodeToString(digest[:]), Width: width, Height: height,
	}, nil
}

func monoFace(size float64) (font.Face, error) {
	parseFontOnce.Do(func() { parsedFont, parsedFontErr = opentype.Parse(gomono.TTF) })
	if parsedFontErr != nil {
		return nil, parsedFontErr
	}
	return opentype.NewFace(parsedFont, &opentype.FaceOptions{
		Size: size, DPI: 72, Hinting: font.HintingFull,
	})
}

func withDefaults(options Options) Options {
	if options.FontSize <= 0 {
		options.FontSize = 20
	}
	if options.FontSize < 8 {
		options.FontSize = 8
	}
	if options.FontSize > 48 {
		options.FontSize = 48
	}
	if options.Padding <= 0 {
		options.Padding = 16
	}
	setDefault(&options.MaxInputBytes, DefaultMaxInputBytes)
	setDefault(&options.MaxPNGBytes, DefaultMaxPNGBytes)
	setDefault(&options.MaxColumns, DefaultMaxColumns)
	setDefault(&options.MaxLines, DefaultMaxLines)
	setDefault(&options.MaxWidth, DefaultMaxWidth)
	setDefault(&options.MaxHeight, DefaultMaxHeight)
	return options
}

func setDefault(value *int, fallback int) {
	if *value <= 0 {
		*value = fallback
	}
}

func fixedInt(value int) fixed.Int26_6 { return fixed.Int26_6(value << 6) }
