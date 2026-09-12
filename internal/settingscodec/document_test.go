package settingscodec

import "testing"

func TestRequireVersionFieldsSeparatesLegacyAndSatelliteSchemas(t *testing.T) {
	common := map[string]struct{}{
		"archive_recommendations":   {},
		"default_providers":         {},
		"default_workdirs":          {},
		"preprocessing_instruction": {},
		"session_naming_enabled":    {},
	}

	legacy := cloneFields(common)
	legacy["preprocessing_enabled"] = struct{}{}
	if err := RequireVersionFields(legacy, 5); err != nil {
		t.Fatalf("valid v5 fields rejected: %v", err)
	}
	if err := RequireVersionFields(legacy, 6); err == nil {
		t.Fatal("v6 accepted legacy preprocessing field")
	}

	satellite := cloneFields(common)
	satellite["satellite_preprocessing_mode"] = struct{}{}
	if err := RequireVersionFields(satellite, 6); err != nil {
		t.Fatalf("valid v6 fields rejected: %v", err)
	}
	if err := RequireVersionFields(satellite, 5); err == nil {
		t.Fatal("v5 accepted satellite preprocessing field")
	}
}

func TestInspectRejectsDuplicateAndUnknownSatelliteFields(t *testing.T) {
	const prefix = `{"version":6,"revision":1,"continue_existing":true,"screen_enabled":false,"card_detail":"standard","show_technical_actions":true,"notify_background_questions":false,"notify_background_errors":true,"session_lifetime":"12h","queue_limit":32,"voice_recognition":"parakeet","retry_undelivered_files":false,`
	for name, suffix := range map[string]string{
		"duplicate": `"satellite_preprocessing_mode":"shared","satellite_preprocessing_mode":"per_session"}`,
		"unknown":   `"satellite_preprocessing_mode":"shared","satellite_preprocessing_scope":"shared"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect([]byte(prefix + suffix)); err == nil {
				t.Fatal("invalid settings fields accepted")
			}
		})
	}
}

func TestInspectAllowsOptionalScreenImageProfileAndRejectsDuplicateOrUnknownField(t *testing.T) {
	const prefix = `{"version":6,"revision":1,"continue_existing":true,"screen_enabled":false,"card_detail":"standard","show_technical_actions":true,"notify_background_questions":false,"notify_background_errors":true,"session_lifetime":"12h","queue_limit":32,"voice_recognition":"parakeet","retry_undelivered_files":false,"archive_recommendations":false,"default_providers":{},"default_workdirs":{},"satellite_preprocessing_mode":"shared","preprocessing_instruction":"","session_naming_enabled":false`
	for name, suffix := range map[string]string{
		"legacy absence": `}`,
		"present":        `,"screen_image_profile":"full_8"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect([]byte(prefix + suffix)); err != nil {
				t.Fatalf("valid document rejected: %v", err)
			}
		})
	}
	for name, suffix := range map[string]string{
		"duplicate":         `,"screen_image_profile":"full_8","screen_image_profile":"current"}`,
		"escaped duplicate": `,"screen_image_profile":"full_8","screen_image_profil\u0065":"current"}`,
		"unknown":           `,"screen_image_profiles":"full_8"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect([]byte(prefix + suffix)); err == nil {
				t.Fatal("invalid screen image profile field accepted")
			}
		})
	}
}

func cloneFields(source map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(source))
	for key := range source {
		result[key] = struct{}{}
	}
	return result
}
