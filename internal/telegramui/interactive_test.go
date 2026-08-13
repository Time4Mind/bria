package telegramui_test

import (
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestInteractiveKeyboardMatchesCCBotLayout(t *testing.T) {
	tokens := make(map[telegramui.Action]telegramui.OpaqueToken)
	for _, action := range []telegramui.Action{
		telegramui.ActionKeySpace, telegramui.ActionKeyUp, telegramui.ActionKeyTab,
		telegramui.ActionKeyLeft, telegramui.ActionKeyDown, telegramui.ActionKeyRight,
		telegramui.ActionKeyEscape, telegramui.ActionKeyCtrlC, telegramui.ActionKeyEnter,
		telegramui.ActionKeyBack,
	} {
		tokens[action] = "token"
	}
	screen := telegramui.RenderInteractiveCard(telegramui.InteractiveInput{
		Copy: i18n.For("en"), Text: "prompt", Control: true, Tokens: tokens,
	})
	grid := telegramui.CanonicalGrid(screen.Grid)
	for _, label := range []string{"␣ Space", "↑", "⇥ Tab", "←", "↓", "→", "⎋ Esc", "^C", "⏎ Enter"} {
		if !strings.Contains(grid, "["+label+" ->") {
			t.Fatalf("missing %q in %s", label, grid)
		}
	}
}

func TestRestoreCheckpointUsesVerticalKeyboard(t *testing.T) {
	screen := telegramui.RenderInteractiveCard(telegramui.InteractiveInput{
		Copy: i18n.For("en"), Text: "restore", Control: true, VerticalOnly: true,
		Tokens: map[telegramui.Action]telegramui.OpaqueToken{
			telegramui.ActionKeyUp: "token", telegramui.ActionKeyDown: "token",
			telegramui.ActionKeySpace: "token", telegramui.ActionKeyTab: "token",
			telegramui.ActionKeyEscape: "token",
			telegramui.ActionKeyCtrlC:  "token", telegramui.ActionKeyEnter: "token",
			telegramui.ActionKeyBack: "token",
		},
	})
	grid := telegramui.CanonicalGrid(screen.Grid)
	if strings.Contains(grid, "key_left") || !strings.Contains(grid, "key_up") ||
		!strings.Contains(grid, "key_down") {
		t.Fatalf("grid=%s", grid)
	}
}

func TestViewOnlyInteractiveCardContainsNoTerminalKeys(t *testing.T) {
	screen := telegramui.RenderInteractiveCard(telegramui.InteractiveInput{
		Copy: i18n.For("en"), Text: "prompt", Control: false,
		Tokens: map[telegramui.Action]telegramui.OpaqueToken{
			telegramui.ActionKeyBack: "token",
		},
	})
	grid := telegramui.CanonicalGrid(screen.Grid)
	if strings.Contains(grid, "key_enter") || !strings.Contains(grid, "key_back") ||
		!strings.Contains(screen.Text, "View only") {
		t.Fatalf("screen=%#v grid=%s", screen, grid)
	}
}
