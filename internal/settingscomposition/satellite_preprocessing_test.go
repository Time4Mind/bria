package settingscomposition_test

import (
	"context"
	"testing"

	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/settingsport"
)

func TestSetSatellitePreprocessingModePersistsTypedSelection(t *testing.T) {
	store := settings.NewMemoryStore()
	preferences := settingscomposition.Preferences{Store: store}
	capability, ok := any(preferences).(settingsport.SatellitePreprocessingPreferences)
	if !ok {
		t.Fatal("preferences do not expose satellite preprocessing selection")
	}
	if err := capability.SetSatellitePreprocessingMode(context.Background(), settingsport.SatellitePreprocessingPerSession); err != nil {
		t.Fatal(err)
	}

	snapshot, err := preferences.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SatellitePreprocessingMode != settingsport.SatellitePreprocessingPerSession || !snapshot.PreprocessingEnabled {
		t.Fatalf("snapshot mode=%q enabled=%t", snapshot.SatellitePreprocessingMode, snapshot.PreprocessingEnabled)
	}
	stored, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.SatellitePreprocessingMode != settings.SatellitePreprocessingPerSession {
		t.Fatalf("stored mode=%q", stored.SatellitePreprocessingMode)
	}
}

func TestSetSatellitePreprocessingModeRejectsUnknownWithoutMutation(t *testing.T) {
	store := settings.NewMemoryStore()
	preferences := settingscomposition.Preferences{Store: store}
	before, _ := store.Load(context.Background())

	err := preferences.SetSatellitePreprocessingMode(context.Background(), "sometimes")
	if err == nil {
		t.Fatal("unknown mode accepted")
	}
	after, _ := store.Load(context.Background())
	if after.SatellitePreprocessingMode != before.SatellitePreprocessingMode {
		t.Fatalf("invalid mode mutated settings: before=%q after=%q", before.SatellitePreprocessingMode, after.SatellitePreprocessingMode)
	}
}
