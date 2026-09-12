package settingscomposition_test

import (
	"context"
	"testing"

	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/settingsport"
)

func TestScreenImageProfileCyclePersistsTypedSequence(t *testing.T) {
	store := settings.NewMemoryStore()
	preferences := settingscomposition.Preferences{Store: store}
	capability, ok := any(preferences).(settingsport.ScreenImagePreferences)
	if !ok {
		t.Fatal("Preferences does not expose ScreenImagePreferences")
	}
	for _, want := range []settings.ScreenImageProfile{
		settings.ScreenImageProfileCompact8,
		settings.ScreenImageProfileCurrent,
		settings.ScreenImageProfileFull8,
	} {
		if err := capability.CycleScreenImageProfile(context.Background()); err != nil {
			t.Fatal(err)
		}
		stored, err := store.Load(context.Background())
		projected, snapshotErr := preferences.Snapshot(context.Background())
		if err != nil || snapshotErr != nil || stored.ScreenImageProfile != want || projected.ScreenImageProfile != string(want) {
			t.Fatalf("stored=%q projected=%q want=%q errors=(%v,%v)", stored.ScreenImageProfile, projected.ScreenImageProfile, want, err, snapshotErr)
		}
	}
}
