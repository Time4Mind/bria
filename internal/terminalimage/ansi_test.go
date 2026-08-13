package terminalimage

import (
	"image/color"
	"testing"
)

func TestParseANSISupportsBasicIndexedAndRGBColors(t *testing.T) {
	lines := parseANSI("\x1b[31mR\x1b[38;5;46mG\x1b[48;2;1;2;3mB\x1b[0m!", 10, 20)
	if len(lines) != 1 || len(lines[0]) != 4 {
		t.Fatalf("parsed lines = %#v", lines)
	}
	assertColor(t, lines[0][0].style.foreground, ansi16[1])
	assertColor(t, lines[0][1].style.foreground, xtermColor(46))
	if got := lines[0][2].style.background; got != (color.RGBA{1, 2, 3, 255}) {
		t.Fatalf("RGB background = %#v", got)
	}
	assertColor(t, lines[0][3].style.foreground, defaultForeground)
}

func TestParseANSIDiscardsControlsAndBoundsPane(t *testing.T) {
	lines := parseANSI("one\x1b]0;secret\a\n123456\x1b[2J", 2, 4)
	if got := plainLines(lines); got != "one\n1234" {
		t.Fatalf("plain pane = %q", got)
	}
}

func assertColor(t *testing.T, got, want color.RGBA) {
	t.Helper()
	if got != want {
		t.Fatalf("color = %#v, want %#v", got, want)
	}
}

func plainLines(lines [][]styledRune) string {
	result := ""
	for index, line := range lines {
		if index > 0 {
			result += "\n"
		}
		for _, item := range line {
			result += string(item.value)
		}
	}
	return result
}
