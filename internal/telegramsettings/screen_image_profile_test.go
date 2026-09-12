package telegramsettings_test

import (
	"context"
	"path/filepath"
	"testing"

	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/settingsport"
	"bria/internal/telegramsettings"
)

type screenImageProfilePreferences struct {
	settingsport.Preferences
	cycles int
}

func TestApplyScreenImageProfilePersistsThroughPublicSettingsBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	preferences := settingscomposition.Preferences{Store: store}
	if err := telegramsettings.Apply(context.Background(), preferences, nil, "settings_screen_image_profile"); err != nil {
		t.Fatal(err)
	}
	reopened, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load(context.Background())
	if err != nil || got.ScreenImageProfile != settings.ScreenImageProfileCompact8 {
		t.Fatalf("persisted profile=%q, want compact_8 (err=%v)", got.ScreenImageProfile, err)
	}
}

func (p *screenImageProfilePreferences) CycleScreenImageProfile(context.Context) error {
	p.cycles++
	return nil
}

func TestApplyCyclesScreenImageProfile(t *testing.T) {
	preferences := &screenImageProfilePreferences{}
	if err := telegramsettings.Apply(context.Background(), preferences, nil, "settings_screen_image_profile"); err != nil {
		t.Fatal(err)
	}
	if preferences.cycles != 1 {
		t.Fatalf("profile cycles = %d, want 1", preferences.cycles)
	}
}
