package terminalimage

import (
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ansi16 = [16]color.RGBA{
	{0, 0, 0, 255}, {205, 49, 49, 255}, {13, 188, 121, 255}, {229, 229, 16, 255},
	{36, 114, 200, 255}, {188, 63, 188, 255}, {17, 168, 205, 255}, {229, 229, 229, 255},
	{102, 102, 102, 255}, {241, 76, 76, 255}, {35, 209, 139, 255}, {245, 245, 67, 255},
	{59, 142, 234, 255}, {214, 112, 214, 255}, {41, 184, 219, 255}, {255, 255, 255, 255},
}

var (
	defaultForeground = color.RGBA{212, 212, 212, 255}
	defaultBackground = color.RGBA{30, 30, 30, 255}
)

type style struct {
	foreground color.RGBA
	background color.RGBA
	customBG   bool
	bold       bool
	inverse    bool
}

func defaultStyle() style { return style{foreground: defaultForeground} }

func (s style) colors() (color.RGBA, color.RGBA) {
	foreground, background := s.foreground, defaultBackground
	if s.customBG {
		background = s.background
	}
	if s.inverse {
		return background, foreground
	}
	return foreground, background
}

type styledRune struct {
	value rune
	style style
}

// parseANSI keeps only printable terminal content and SGR state. Other CSI
// and OSC controls are discarded so untrusted pane output cannot reach PNG
// encoders or Telegram as control data.
func parseANSI(text string, maxLines, maxColumns int) [][]styledRune {
	if maxLines < 1 || maxColumns < 1 {
		return nil
	}
	// Keep the most recent bounded viewport. Blank rows before the first and
	// after the last visible cell are discarded; otherwise a temporarily tall
	// detached tmux pane turns into a mostly empty portrait image on phones.
	ring := make([][]styledRune, maxLines)
	head, count, pendingBlanks := 0, 0, 0
	currentLine := make([]styledRune, 0, min(maxColumns, 80))
	appendLine := func(line []styledRune) {
		cloned := append([]styledRune(nil), line...)
		if count < maxLines {
			ring[(head+count)%maxLines] = cloned
			count++
			return
		}
		ring[head] = cloned
		head = (head + 1) % maxLines
	}
	finishLine := func() {
		if visibleANSI(currentLine) {
			for pendingBlanks > 0 {
				appendLine(nil)
				pendingBlanks--
			}
			appendLine(currentLine)
		} else if count > 0 && pendingBlanks < maxLines {
			pendingBlanks++
		}
		currentLine = currentLine[:0]
	}
	current := defaultStyle()
	for index := 0; index < len(text); {
		if text[index] == 0x1b {
			index = consumeEscape(text, index, &current)
			continue
		}
		r, size := rune(text[index]), 1
		if r >= 0x80 {
			r, size = decodeRune(text[index:])
		}
		index += size
		switch r {
		case '\n':
			finishLine()
		case '\r', '\b':
			continue
		case '\t':
			spaces := 4 - len(currentLine)%4
			for range spaces {
				if len(currentLine) == maxColumns {
					break
				}
				currentLine = append(currentLine, styledRune{' ', current})
			}
		default:
			if (r >= 0x20 && r != 0x7f) && len(currentLine) < maxColumns {
				currentLine = append(currentLine, styledRune{r, current})
			}
		}
	}
	finishLine()
	lines := make([][]styledRune, count)
	for index := range count {
		lines[index] = ring[(head+index)%maxLines]
	}
	return lines
}

func visibleANSI(line []styledRune) bool {
	for _, item := range line {
		if item.value != ' ' || item.style.customBG {
			return true
		}
	}
	return false
}

func decodeRune(value string) (rune, int) {
	valueRune, size := utf8.DecodeRuneInString(value)
	return valueRune, size
}

func consumeEscape(text string, start int, current *style) int {
	if start+1 >= len(text) {
		return len(text)
	}
	switch text[start+1] {
	case '[':
		for index := start + 2; index < len(text) && index-start <= 64; index++ {
			if text[index] >= 0x40 && text[index] <= 0x7e {
				if text[index] == 'm' {
					applySGR(current, text[start+2:index])
				}
				return index + 1
			}
		}
	case ']':
		for index := start + 2; index < len(text) && index-start <= 4096; index++ {
			if text[index] == '\a' {
				return index + 1
			}
			if text[index] == 0x1b && index+1 < len(text) && text[index+1] == '\\' {
				return index + 2
			}
		}
	}
	return start + 2
}

func applySGR(current *style, sequence string) {
	parts := []int{0}
	if sequence != "" {
		parts = parts[:0]
		for _, raw := range strings.Split(sequence, ";") {
			value, err := strconv.Atoi(raw)
			if err != nil {
				value = 0
			}
			parts = append(parts, value)
		}
	}
	for index := 0; index < len(parts); index++ {
		code := parts[index]
		switch {
		case code == 0:
			*current = defaultStyle()
		case code == 1:
			current.bold = true
		case code == 22:
			current.bold = false
		case code == 7:
			current.inverse = true
		case code == 27:
			current.inverse = false
		case code >= 30 && code <= 37:
			current.foreground = ansi16[code-30+boldOffset(*current)]
		case code >= 90 && code <= 97:
			current.foreground = ansi16[code-90+8]
		case code == 39:
			current.foreground = defaultForeground
		case code >= 40 && code <= 47:
			current.background, current.customBG = ansi16[code-40], true
		case code >= 100 && code <= 107:
			current.background, current.customBG = ansi16[code-100+8], true
		case code == 49:
			current.customBG = false
		case code == 38 || code == 48:
			parsed, consumed, ok := extendedColor(parts[index+1:])
			if ok {
				if code == 38 {
					current.foreground = parsed
				} else {
					current.background, current.customBG = parsed, true
				}
				index += consumed
			}
		}
	}
}

func boldOffset(current style) int {
	if current.bold {
		return 8
	}
	return 0
}

func extendedColor(parts []int) (color.RGBA, int, bool) {
	if len(parts) >= 2 && parts[0] == 5 && parts[1] >= 0 && parts[1] <= 255 {
		return xtermColor(parts[1]), 2, true
	}
	if len(parts) >= 4 && parts[0] == 2 {
		return color.RGBA{uint8(clamp(parts[1])), uint8(clamp(parts[2])), uint8(clamp(parts[3])), 255}, 4, true
	}
	return color.RGBA{}, 0, false
}

func xtermColor(index int) color.RGBA {
	if index < 16 {
		return ansi16[index]
	}
	if index < 232 {
		index -= 16
		levels := [6]uint8{0, 95, 135, 175, 215, 255}
		return color.RGBA{levels[index/36], levels[index/6%6], levels[index%6], 255}
	}
	gray := uint8(8 + (index-232)*10)
	return color.RGBA{gray, gray, gray, 255}
}

func clamp(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}
