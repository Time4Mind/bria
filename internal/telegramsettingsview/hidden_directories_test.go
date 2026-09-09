package telegramsettingsview

import (
	"context"
	"strings"
	"testing"

	"bria/internal/settingsport"
)

type hiddenDirectoryPreferences struct {
	settingsPreferencesStub
	show bool
}

func (p hiddenDirectoryPreferences) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return settingsport.Snapshot{ShowHiddenDirectories: p.show}, nil
}

func (hiddenDirectoryPreferences) ToggleHiddenDirectories(context.Context) error { return nil }

func TestHiddenDirectoriesCreationControlReflectsPreference(t *testing.T) {
	for _, tc := range []struct {
		show bool
		text string
	}{{false, "скрывать"}, {true, "показывать"}} {
		t.Run(tc.text, func(t *testing.T) {
			surface, err := RenderCategory(context.Background(), hiddenDirectoryPreferences{show: tc.show}, nil, 16, CategoryCreation)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(surface.Text, "| Скрытые каталоги | "+tc.text+" |") {
				t.Fatalf("missing hidden-directory state %q: %s", tc.text, surface.Text)
			}
			count := 0
			for _, row := range surface.Rows {
				for _, button := range row {
					if button.Action == "settings_hidden_directories" {
						count++
						if button.Label != "Скрытые каталоги" || button.Choice != 0 {
							t.Fatalf("invalid toggle: %+v", button)
						}
					}
				}
			}
			if count != 1 {
				t.Fatalf("hidden-directory toggle count=%d", count)
			}
			if surface.Rows[len(surface.Rows)-2][0].Action != "settings_default_workdir" {
				t.Fatal("default directory is no longer the last setting before Back")
			}
		})
	}
	if category, ok := CategoryForAction("settings_hidden_directories"); !ok || category != CategoryCreation {
		t.Fatalf("hidden directories category=%v,%v", category, ok)
	}
}

func TestHiddenDirectoriesControlRequiresOptionalCapability(t *testing.T) {
	for _, p := range []settingsport.Preferences{nil, settingsPreferencesStub{}} {
		surface, err := RenderCategory(context.Background(), p, nil, 16, CategoryCreation)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(surface.Text, "| Скрытые каталоги | скрывать |") {
			t.Fatalf("missing default visibility: %s", surface.Text)
		}
		for _, row := range surface.Rows {
			for _, button := range row {
				if button.Action == "settings_hidden_directories" {
					t.Fatal("unsupported hidden-directory mutation advertised")
				}
			}
		}
	}
}
