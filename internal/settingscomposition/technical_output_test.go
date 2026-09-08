package settingscomposition_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramsettingsview"
)

func TestTechnicalOutputLinesPublicUICyclePersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	preferences := settingscomposition.Preferences{Store: store}
	c, err := telegramcontroller.New(42, 42, "local", settingsControllerCreator{}, settingsControllerSessions{}, settingsControllerSubmit{}, settingsControllerNotifier{}, telegramcontroller.Options{Settings: preferences})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	result, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticSettingsCategory, Choice: int(telegramsettingsview.CategoryCard)})
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range []int{10, 20, 3, 5, 10} {
		if index > 0 {
			result, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: "settings_technical_output_lines"})
			if err != nil {
				t.Fatal(err)
			}
		}
		if result.Surface == nil || !strings.Contains(result.Surface.Text, fmt.Sprintf("| Строки технического вывода | %d |", want)) {
			t.Fatalf("line choice %d missing: %+v", want, result.Surface)
		}
		var action telegramcontroller.SemanticActionKind
		for _, row := range result.Surface.Rows {
			for _, button := range row {
				if button.Action == "settings_technical_output_lines" {
					action = button.Action
				}
			}
		}
		if action == "" {
			t.Fatal("technical output lines control missing")
		}
		if index > 0 {
			// Every mutation must be visible after opening a fresh store.
			reopened, err := settings.OpenFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			surface, err := telegramsettingsview.RenderCategory(ctx, settingscomposition.Preferences{Store: reopened}, nil, 32, telegramsettingsview.CategoryCard)
			if err != nil || !strings.Contains(surface.Text, fmt.Sprintf("| Строки технического вывода | %d |", want)) {
				t.Fatalf("reopened choice %d missing: %s, %v", want, surface.Text, err)
			}
		}
	}
}
