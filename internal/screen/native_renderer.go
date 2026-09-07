package screen

// Native renderer: a bounded terminal capture rendered with an embedded
// monospace font. Unlike the event projection used by Store, this preserves
// ANSI SGR foreground/background colours for the explicit screenshot action.

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	nativeFontSize    = 20.0
	nativePadding     = 16
	nativeBaseLines   = 48
	nativeBaseColumns = 200
)

type nativeLimits struct {
	captureBytes, maxLines, maxColumns, maxPNGBytes int
}

func nativeLimitsFor(options NativeOptions) (nativeLimits, error) {
	captureKiB := options.CaptureKiB
	if captureKiB == 0 {
		captureKiB = DefaultNativeCaptureKiB
	}
	if captureKiB != 48 && captureKiB != 64 && captureKiB != 86 {
		return nativeLimits{}, ErrInvalidNativeOptions
	}
	// Both axes scale with the selected raw capture budget. Integer ceiling
	// keeps 64 and 86 KiB from receiving less than their proportional share.
	scale := func(base int) int {
		return (base*captureKiB + DefaultNativeCaptureKiB - 1) / DefaultNativeCaptureKiB
	}
	return nativeLimits{
		captureBytes: captureKiB << 10,
		maxLines:     scale(nativeBaseLines),
		maxColumns:   scale(nativeBaseColumns),
		maxPNGBytes:  NativeMaxPNGBytes,
	}, nil
}

type nativeStyle struct {
	fg, bg                  color.RGBA
	customBG, inverse, bold bool
}
type nativeCell struct {
	value rune
	style nativeStyle
}

var (
	nativeFontOnce   sync.Once
	nativeParsedFont *opentype.Font
	nativeFontErr    error
)

var nativeANSI = [16]color.RGBA{
	{0, 0, 0, 255}, {205, 49, 49, 255}, {13, 188, 121, 255}, {229, 229, 16, 255},
	{36, 114, 200, 255}, {188, 63, 188, 255}, {17, 168, 205, 255}, {229, 229, 229, 255},
	{102, 102, 102, 255}, {241, 76, 76, 255}, {35, 209, 139, 255}, {245, 245, 67, 255},
	{59, 142, 234, 255}, {214, 112, 214, 255}, {41, 184, 219, 255}, {255, 255, 255, 255},
}

var nativeDefaultFG = color.RGBA{212, 212, 212, 255}
var nativeDefaultBG = color.RGBA{30, 30, 30, 255}

func nativeDefaultStyle() nativeStyle { return nativeStyle{fg: nativeDefaultFG} }

func (style nativeStyle) colors() (color.RGBA, color.RGBA) {
	fg, bg := style.fg, nativeDefaultBG
	if style.customBG {
		bg = style.bg
	}
	if style.inverse {
		return bg, fg
	}
	return fg, bg
}

func renderNativeANSI(text string, limits nativeLimits) ([]byte, int, int, error) {
	lines := parseNativeANSI(text, limits.maxLines, limits.maxColumns)
	lines = focusNativeCodexActivity(lines)
	if len(lines) == 0 {
		return nil, 0, 0, ErrInvalidEvent
	}
	nativeFontOnce.Do(func() { nativeParsedFont, nativeFontErr = opentype.Parse(gomono.TTF) })
	if nativeFontErr != nil {
		return nil, 0, 0, nativeFontErr
	}
	face, err := opentype.NewFace(nativeParsedFont, &opentype.FaceOptions{Size: nativeFontSize, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, 0, 0, err
	}
	defer face.Close()

	data, width, height, err := encodeNativePNG(lines, face)
	if err != nil || len(data) <= limits.maxPNGBytes {
		return data, width, height, err
	}

	// Preserve the largest suffix that fits. Each probe renders parsed cells,
	// so neither UTF-8 nor ANSI escape sequences can be cut in the middle.
	bad, good := 0, len(lines)-1
	data, width, height, err = encodeNativePNG(lines[good:], face)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(data) > limits.maxPNGBytes {
		return trimNativeColumnsToPNGSize(lines[good], face, limits.maxPNGBytes)
	}
	for good-bad > 1 {
		middle := bad + (good-bad)/2
		candidate, candidateWidth, candidateHeight, candidateErr := encodeNativePNG(lines[middle:], face)
		if candidateErr != nil {
			return nil, 0, 0, candidateErr
		}
		if len(candidate) <= limits.maxPNGBytes {
			good = middle
			data, width, height = candidate, candidateWidth, candidateHeight
		} else {
			bad = middle
		}
	}
	return data, width, height, nil
}

func encodeNativePNG(lines [][]nativeCell, face font.Face) ([]byte, int, int, error) {
	metrics := face.Metrics()
	cellWidth := max(1, font.MeasureString(face, "M").Ceil())
	lineHeight := max(1, metrics.Height.Round()+2)
	columns := 1
	for _, line := range lines {
		columns = max(columns, len(line))
	}
	// Geometry follows useful payload only. Do not stretch glyphs or pad narrow
	// captures merely to reach a preferred aspect ratio.
	width := columns*cellWidth + 2*nativePadding
	height := len(lines)*lineHeight + 2*nativePadding
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(nativeDefaultBG), image.Point{}, draw.Src)
	drawer := font.Drawer{Dst: canvas, Face: face}
	for row, line := range lines {
		y := nativePadding + row*lineHeight
		for column, cell := range line {
			x := nativePadding + column*cellWidth
			if x+cellWidth > width || y+lineHeight > height {
				break
			}
			fg, bg := cell.style.colors()
			draw.Draw(canvas, image.Rect(x, y, x+cellWidth, y+lineHeight), image.NewUniform(bg), image.Point{}, draw.Src)
			drawer.Src = image.NewUniform(fg)
			drawer.Dot = fixed.Point26_6{X: nativeFixedInt(x), Y: nativeFixedInt(nativePadding + metrics.Ascent.Round() + row*lineHeight)}
			drawer.DrawString(string(cell.value))
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		return nil, 0, 0, err
	}
	return output.Bytes(), width, height, nil
}

func trimNativeColumnsToPNGSize(line []nativeCell, face font.Face, maxBytes int) ([]byte, int, int, error) {
	if len(line) == 0 {
		return nil, 0, 0, ErrInvalidEvent
	}
	low, high := 1, len(line)
	var best []byte
	bestWidth, bestHeight := 0, 0
	for low <= high {
		middle := low + (high-low)/2
		candidate, width, height, err := encodeNativePNG([][]nativeCell{line[:middle]}, face)
		if err != nil {
			return nil, 0, 0, err
		}
		if len(candidate) <= maxBytes {
			best, bestWidth, bestHeight = candidate, width, height
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if len(best) == 0 {
		return nil, 0, 0, ErrEventTooLarge
	}
	return best, bestWidth, bestHeight, nil
}

func parseNativeANSI(text string, maxLines, maxColumns int) [][]nativeCell {
	if maxLines < 1 || maxColumns < 1 {
		return nil
	}
	ring := make([][]nativeCell, maxLines)
	head, count, pendingBlanks := 0, 0, 0
	current := nativeDefaultStyle()
	line := make([]nativeCell, 0, min(maxColumns, 80))
	appendLine := func(value []nativeCell) {
		cloned := append([]nativeCell(nil), value...)
		if count < maxLines {
			ring[(head+count)%maxLines] = cloned
			count++
			return
		}
		ring[head] = cloned
		head = (head + 1) % maxLines
	}
	finishLine := func() {
		if visibleNativeANSI(line) {
			for pendingBlanks > 0 {
				appendLine(nil)
				pendingBlanks--
			}
			appendLine(line)
		} else if count > 0 && pendingBlanks < maxLines {
			pendingBlanks++
		}
		line = line[:0]
	}
	for i := 0; i < len(text); {
		if text[i] == 0x1b {
			i = consumeNativeEscape(text, i, &current)
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		switch r {
		case '\n':
			finishLine()
		case '\r', '\b':
		case '\t':
			for n := 4 - len(line)%4; n > 0 && len(line) < maxColumns; n-- {
				line = append(line, nativeCell{' ', current})
			}
		default:
			if r >= 0x20 && r != 0x7f && len(line) < maxColumns {
				line = append(line, nativeCell{r, current})
			}
		}
	}
	finishLine()
	lines := make([][]nativeCell, count)
	for index := range count {
		lines[index] = ring[(head+index)%maxLines]
	}
	return lines
}

func visibleNativeANSI(line []nativeCell) bool {
	for _, cell := range line {
		if cell.value != ' ' || cell.style.customBG {
			return true
		}
	}
	return false
}

func consumeNativeEscape(text string, start int, current *nativeStyle) int {
	if start+1 >= len(text) {
		return len(text)
	}
	switch text[start+1] {
	case '[':
		for i := start + 2; i < len(text) && i-start <= 64; i++ {
			if text[i] >= 0x40 && text[i] <= 0x7e {
				if text[i] == 'm' {
					applyNativeSGR(current, text[start+2:i])
				}
				return i + 1
			}
		}
	case ']':
		for i := start + 2; i < len(text) && i-start <= 4096; i++ {
			if text[i] == '\a' {
				return i + 1
			}
			if text[i] == 0x1b && i+1 < len(text) && text[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return min(start+2, len(text))
}

func applyNativeSGR(style *nativeStyle, sequence string) {
	parts := []int{0}
	if sequence != "" {
		parts = nil
		for _, raw := range strings.Split(sequence, ";") {
			n, err := strconv.Atoi(raw)
			if err != nil {
				n = 0
			}
			parts = append(parts, n)
		}
	}
	for i := 0; i < len(parts); i++ {
		code := parts[i]
		switch {
		case code == 0:
			*style = nativeDefaultStyle()
		case code == 1:
			style.bold = true
		case code == 22:
			style.bold = false
		case code == 7:
			style.inverse = true
		case code == 27:
			style.inverse = false
		case code >= 30 && code <= 37:
			style.fg = nativeANSI[code-30+boolInt(style.bold)*8]
		case code >= 90 && code <= 97:
			style.fg = nativeANSI[code-90+8]
		case code == 39:
			style.fg = nativeDefaultFG
		case code >= 40 && code <= 47:
			style.bg, style.customBG = nativeANSI[code-40], true
		case code >= 100 && code <= 107:
			style.bg, style.customBG = nativeANSI[code-100+8], true
		case code == 49:
			style.customBG = false
		case code == 38 || code == 48:
			parsed, consumed, ok := nativeExtendedColor(parts[i+1:])
			if ok {
				if code == 38 {
					style.fg = parsed
				} else {
					style.bg, style.customBG = parsed, true
				}
				i += consumed
			}
		}
	}
}

func nativeExtendedColor(parts []int) (color.RGBA, int, bool) {
	if len(parts) >= 2 && parts[0] == 5 && parts[1] >= 0 && parts[1] <= 255 {
		return nativeXtermColor(parts[1]), 2, true
	}
	if len(parts) >= 4 && parts[0] == 2 {
		return color.RGBA{uint8(nativeClamp(parts[1])), uint8(nativeClamp(parts[2])), uint8(nativeClamp(parts[3])), 255}, 4, true
	}
	return color.RGBA{}, 0, false
}

func nativeXtermColor(index int) color.RGBA {
	if index < 16 {
		return nativeANSI[index]
	}
	if index < 232 {
		index -= 16
		levels := [6]uint8{0, 95, 135, 175, 215, 255}
		return color.RGBA{levels[index/36], levels[index/6%6], levels[index%6], 255}
	}
	gray := uint8(8 + (index-232)*10)
	return color.RGBA{gray, gray, gray, 255}
}

func nativeClamp(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}

func focusNativeCodexActivity(lines [][]nativeCell) [][]nativeCell {
	composer := nativeCodexComposer(lines)
	if composer < 0 {
		return lines
	}
	searchFrom := 0
	for index := composer - 1; index >= 0; index-- {
		if strings.HasPrefix(plainNativeLine(lines[index]), "› ") {
			searchFrom = index + 1
			break
		}
	}
	start := -1
	for index := searchFrom; index < composer; index++ {
		line := plainNativeLine(lines[index])
		if !strings.HasPrefix(line, "• ") || strings.HasPrefix(line, "• Queued follow-up inputs") {
			continue
		}
		start = index
		break
	}
	if start < 0 {
		start = composer
	}
	for start < len(lines) && !visibleNativeANSI(lines[start]) {
		start++
	}
	return lines[start:]
}

func nativeCodexComposer(lines [][]nativeCell) int {
	for index := len(lines) - 1; index >= 0; index-- {
		if !strings.HasPrefix(plainNativeLine(lines[index]), "› ") {
			continue
		}
		for footer := index + 1; footer < len(lines); footer++ {
			text := plainNativeLine(lines[footer])
			if strings.Contains(text, " · ") && (strings.Contains(text, "~/") || strings.Contains(text, "context left")) {
				return index
			}
		}
	}
	return -1
}

func plainNativeLine(line []nativeCell) string {
	var builder strings.Builder
	builder.Grow(len(line))
	for _, cell := range line {
		builder.WriteRune(cell.value)
	}
	return builder.String()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nativeFixedInt(value int) fixed.Int26_6 { return fixed.Int26_6(value << 6) }
