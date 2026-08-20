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
		"Which migration? (1/2)\n\n› 1. Incremental",
		"› 1. Incremental - migrate module-by-module\n       while both paths remain available",
		"both paths remain available\n\n─────\n\n  2. Big-bang",
		"one release\n\n─────\n\n  3. Let me describe",
		"another approach\n\nEnter to select",
	} {
		if !strings.Contains(got, boundary) {
			t.Fatalf("missing choice boundary %q in:\n%s", boundary, got)
		}
	}
}

func TestClaudeModelSelectorKeepsOnlyChoiceBoundaries(t *testing.T) {
	raw := "Select model Switch between Claude models.\n\n" +
		"↑ 4. Kimi K3 (256K, recommended)\nCustom Sonnet model\n" +
		"❯ 5. Kimi K2.7 Code (256K) ✔ Custom Haiku model\n\n" +
		"… +3 models\n\nHigh effort (default) ←/→ to adjust\n\n" +
		"Enter to set as default · s to use this session only · Esc to cancel"
	got := telegramui.FormatInteractivePrompt(raw)
	if strings.Count(got, "─────") != 1 ||
		!strings.Contains(got, "recommended)\nCustom Sonnet model\n\n─────\n\n❯ 5.") ||
		!strings.Contains(got, "Custom Haiku model\n\n… +3 models") ||
		strings.Contains(got, "models\n\n─────\n\nHigh effort") {
		t.Fatalf("model selector=%q", got)
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
