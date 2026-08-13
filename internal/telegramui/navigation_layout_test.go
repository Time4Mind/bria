package telegramui

import (
	"testing"

	"github.com/Time4Mind/bria/internal/i18n"
)

func TestMenuAndExitUseSeparateFooterRows(t *testing.T) {
	copy := i18n.For("ru")
	tests := []struct {
		name       string
		screen     Screen
		exitAction Action
	}{
		{
			name:       "directory picker",
			screen:     RenderCreateDirectories(copy, "/work", nil, 1, 1),
			exitAction: ActionSessions,
		},
		{
			name:       "resume picker",
			screen:     RenderCreateResumePage(copy, "/work", nil, 0, 1, 1),
			exitAction: ActionNewDirectoryBack,
		},
		{
			name: "interactive card",
			screen: RenderInteractiveCard(InteractiveInput{
				Copy: copy, Text: "prompt", Tokens: map[Action]OpaqueToken{ActionKeyBack: "back"},
			}),
			exitAction: ActionKeyBack,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMenuAboveExit(t, test.screen.Grid, test.exitAction)
		})
	}
}

func TestDirectorySelectIsImmediatelyLeftOfMenu(t *testing.T) {
	screen := RenderCreateDirectories(i18n.For("ru"), "/work", nil, 1, 1)
	actions := screen.Grid[len(screen.Grid)-2]
	if len(actions) != 3 ||
		actions[1].Callback.Action != ActionNewDirectoryPick ||
		actions[2].Callback.Action != ActionMenu {
		t.Fatalf("directory footer=%s", CanonicalGrid(screen.Grid))
	}
}

func assertMenuAboveExit(t *testing.T, grid Grid, exitAction Action) {
	t.Helper()
	if len(grid) < 2 {
		t.Fatalf("footer has fewer than two rows: %s", CanonicalGrid(grid))
	}
	actions := grid[len(grid)-2]
	exit := grid[len(grid)-1]
	if len(actions) == 0 || actions[len(actions)-1].Callback.Action != ActionMenu {
		t.Fatalf("menu is not the rightmost action: %s", CanonicalGrid(grid))
	}
	if len(exit) != 1 || exit[0].Callback.Action != exitAction {
		t.Fatalf("exit is not alone on the bottom row: %s", CanonicalGrid(grid))
	}
}
