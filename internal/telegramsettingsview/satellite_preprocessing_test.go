package telegramsettingsview

import (
	"context"
	"reflect"
	"testing"

	"bria/internal/settingsport"
)

type satellitePreferencesStub struct{ settingsPreferencesStub }

func (satellitePreferencesStub) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return settingsport.Snapshot{
		ContinueExisting: true, CardDetail: "standard", CardPageLimit: 64,
		ShowTechnicalActions: true, NotifyBackgroundErrors: true,
		SessionLifetime: "never", QueueLimit: 16, VoiceRecognition: "parakeet",
		SatellitePreprocessingMode: settingsport.SatellitePreprocessingPerSession,
	}, nil
}

func (satellitePreferencesStub) SetSatellitePreprocessingMode(context.Context, settingsport.SatellitePreprocessingMode) error {
	return nil
}

func TestPreprocessingCategorySelectsSatelliteMode(t *testing.T) {
	surface, err := RenderCategory(context.Background(), satellitePreferencesStub{}, nil, 16, CategoryPreprocessing)
	if err != nil {
		t.Fatal(err)
	}
	if !stringsContains(surface.Text, "| Режим сателлита | На сессию |") {
		t.Fatalf("surface text=%q", surface.Text)
	}
	want := []string{
		"settings_preprocessing_disabled",
		"settings_preprocessing_shared",
		"settings_preprocessing_per_session",
		"settings_preprocessing_instruction",
		"settings_preprocessing_reset",
		"menu_settings",
	}
	var got []string
	for _, row := range surface.Rows {
		for _, button := range row {
			got = append(got, button.Action)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actions=%v want=%v", got, want)
	}
	for _, action := range want[:3] {
		if category, ok := CategoryForAction(action); !ok || category != CategoryPreprocessing {
			t.Fatalf("action %q routes to (%v,%t)", action, category, ok)
		}
	}
}

func TestPreprocessingCategoryWithoutStoreShowsSharedProductDefault(t *testing.T) {
	surface, err := RenderCategory(context.Background(), nil, nil, 16, CategoryPreprocessing)
	if err != nil {
		t.Fatal(err)
	}
	if !stringsContains(surface.Text, "| Режим сателлита | Общий |") {
		t.Fatalf("surface text=%q", surface.Text)
	}
}
