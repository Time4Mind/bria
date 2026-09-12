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

func TestSessionButtonsShowsScreenImageProfileAndCycleControl(t *testing.T) {
	for _, test := range []struct{ profile, label string }{
		{"", "100%, 8 цветов"},
		{"full_8", "100%, 8 цветов"},
		{"compact_8", "75%, 8 цветов"},
		{"current", "100%, полная палитра"},
	} {
		surface, err := RenderCategory(context.Background(), screenImageProfilePreferences{profile: test.profile}, nil, 16, CategorySessionButtons)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(surface.Text, "| Изображение Screen | "+test.label+" |") {
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
