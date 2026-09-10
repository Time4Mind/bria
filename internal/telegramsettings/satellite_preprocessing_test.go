package telegramsettings_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/settingsport"
	"bria/internal/telegramsettings"
)

type satellitePreprocessingPreferences struct {
	settingsport.Preferences
	mode          settingsport.SatellitePreprocessingMode
	err           error
	legacyToggles int
}

func (p *satellitePreprocessingPreferences) SetSatellitePreprocessingMode(_ context.Context, mode settingsport.SatellitePreprocessingMode) error {
	if p.err != nil {
		return p.err
	}
	p.mode = mode
	return nil
}

func (p *satellitePreprocessingPreferences) TogglePreprocessing(context.Context) error {
	p.legacyToggles++
	return nil
}

func (p *satellitePreprocessingPreferences) SetPreprocessingInstruction(context.Context, string) error {
	return nil
}

func TestApplySelectsExactSatellitePreprocessingMode(t *testing.T) {
	preferences := &satellitePreprocessingPreferences{}
	for _, tc := range []struct {
		action string
		want   settingsport.SatellitePreprocessingMode
	}{
		{"settings_preprocessing_disabled", settingsport.SatellitePreprocessingDisabled},
		{"settings_preprocessing_shared", settingsport.SatellitePreprocessingShared},
		{"settings_preprocessing_per_session", settingsport.SatellitePreprocessingPerSession},
	} {
		if err := telegramsettings.Apply(context.Background(), preferences, nil, tc.action); err != nil {
			t.Fatalf("apply %q: %v", tc.action, err)
		}
		if preferences.mode != tc.want {
			t.Fatalf("apply %q mode=%q want=%q", tc.action, preferences.mode, tc.want)
		}
	}
	if preferences.legacyToggles != 0 {
		t.Fatalf("exact mode actions used legacy toggle %d times", preferences.legacyToggles)
	}
}

func TestApplySatellitePreprocessingModePreservesSetterErrorAndLastMode(t *testing.T) {
	saveErr := errors.New("save failed")
	preferences := &satellitePreprocessingPreferences{
		mode: settingsport.SatellitePreprocessingShared,
		err:  saveErr,
	}

	err := telegramsettings.Apply(context.Background(), preferences, nil, "settings_preprocessing_per_session")
	if !errors.Is(err, saveErr) {
		t.Fatalf("error=%v want=%v", err, saveErr)
	}
	if preferences.mode != settingsport.SatellitePreprocessingShared {
		t.Fatalf("failed save changed mode to %q", preferences.mode)
	}
	if preferences.legacyToggles != 0 {
		t.Fatalf("failed typed save fell back to legacy toggle %d times", preferences.legacyToggles)
	}
}

func TestApplySatellitePreprocessingModeRequiresTypedCapability(t *testing.T) {
	preferences := struct{ settingsport.Preferences }{}
	if err := telegramsettings.Apply(context.Background(), preferences, nil, "settings_preprocessing_shared"); err == nil {
		t.Fatal("missing typed satellite preprocessing capability reported success")
	}
}
