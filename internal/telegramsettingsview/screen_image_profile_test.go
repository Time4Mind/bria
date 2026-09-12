package telegramsettingsview

import (
	"context"
	"strings"
	"testing"

	"bria/internal/settingsport"
)

type screenImageProfilePreferences struct {
	settingsPreferencesStub
	profile string
}

func (p screenImageProfilePreferences) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return settingsport.Snapshot{ScreenImageProfile: p.profile}, nil
}

func (screenImageProfilePreferences) CycleScreenImageProfile(context.Context) error { return nil }
func (screenImageProfilePreferences) CycleScreenCaptureLimit(context.Context) error { return nil }

func TestCardContentShowsScreenImageProfileAndCycleControl(t *testing.T) {
	for _, test := range []struct{ profile, label string }{
		{"", "100%, 8 цветов"},
		{"full_8", "100%, 8 цветов"},
		{"compact_8", "75%, 8 цветов"},
		{"current", "100%, полная палитра"},
	} {
		surface, err := RenderCategory(context.Background(), screenImageProfilePreferences{profile: test.profile}, nil, 16, CategoryCard)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(surface.Text, "| Качество скрина | "+test.label+" |") {
			t.Fatalf("profile %q missing from surface: %s", test.profile, surface.Text)
		}
		found := false
		for _, row := range surface.Rows {
			for _, button := range row {
				if button.Action == "settings_screen_image_profile" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("profile cycle button missing: %#v", surface.Rows)
		}
	}
}

func TestScreenSettingsActionsReturnToTheirSemanticCategories(t *testing.T) {
	for action, want := range map[string]Category{
		"settings_screen":               CategorySessionButtons,
		"settings_screen_capture_limit": CategoryCard,
		"settings_screen_image_profile": CategoryCard,
	} {
		if got, ok := CategoryForAction(action); !ok || got != want {
			t.Fatalf("CategoryForAction(%q) = (%v, %v), want (%v, true)", action, got, ok, want)
		}
	}
}
