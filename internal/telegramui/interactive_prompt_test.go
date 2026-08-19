package telegramui_test

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestInteractiveChoicesUseCCBotBlockLayout(t *testing.T) {
	raw := "Which migration? (1/2)\n\n" +
		"› 1. Incremental - migrate module-by-module\n" +
		"       while both paths remain available\n" +
		"  2. Big-bang - cut over in one release\n" +
		"  3. Let me describe another approach\n\n" +
		"Enter to select · Esc to cancel\n"
	got := telegramui.FormatInteractivePrompt(raw)
	for _, boundary := range []string{
		"Which migration? (1/2)\n\n─────\n\n› 1. Incremental",
		"both paths remain available\n\n─────\n\n  2. Big-bang",
		"one release\n\n─────\n\n  3. Let me describe",
		"another approach\n\n─────\n\nEnter to select",
	} {
		if !strings.Contains(got, boundary) {
			t.Fatalf("missing choice boundary %q in:\n%s", boundary, got)
		}
	}
}

func TestInteractiveBoxBordersAreRemovedAndChoicesSeparated(t *testing.T) {
	raw := "┌────────┐\n│ ❯ 1. First │\n├────────┤\n│   2. Second │\n└────────┘"
	got := telegramui.FormatInteractivePrompt(raw)
	if strings.ContainsAny(got, "┌┐└┘│├┤") ||
		!strings.Contains(got, "❯ 1. First\n\n─────\n   2. Second") {
		t.Fatalf("boxed prompt=%q", got)
	}
}
