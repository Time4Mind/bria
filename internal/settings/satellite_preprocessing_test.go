package settings

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestSatellitePreprocessingDefaultsToSharedAndRejectsUnknownMode(t *testing.T) {
	got := Default()
	if got.SatellitePreprocessingMode != SatellitePreprocessingShared || !got.PreprocessingEnabled {
		t.Fatalf("default mode=%q enabled=%t, want shared/true", got.SatellitePreprocessingMode, got.PreprocessingEnabled)
	}

	got.SatellitePreprocessingMode = "sometimes"
	if err := got.Validate(); err == nil {
		t.Fatal("unknown satellite preprocessing mode passed validation")
	}
}

func TestSatellitePreprocessingMigratesLegacyBooleanAndPersistsMode(t *testing.T) {
	for version := 1; version <= 5; version++ {
		for _, test := range []struct {
			name string
			old  string
			want SatellitePreprocessingMode
		}{
			{name: "enabled", old: "true", want: SatellitePreprocessingShared},
			{name: "disabled", old: "false", want: SatellitePreprocessingDisabled},
		} {
			t.Run(test.name+"/v"+strconv.Itoa(version), func(t *testing.T) {
				legacy := legacySatelliteSettingsDocument(version, test.old)

				snapshot, err := Decode(strings.NewReader(legacy))
				if err != nil {
					t.Fatal(err)
				}
				if snapshot.Settings.SatellitePreprocessingMode != test.want || snapshot.Settings.PreprocessingEnabled != (test.want != SatellitePreprocessingDisabled) {
					t.Fatalf("migration mode=%q enabled=%t", snapshot.Settings.SatellitePreprocessingMode, snapshot.Settings.PreprocessingEnabled)
				}

				encoded, err := json.Marshal(documentFromSnapshot(snapshot))
				if err != nil {
					t.Fatal(err)
				}
				wantJSON := `"satellite_preprocessing_mode":"` + string(test.want) + `"`
				compact := bytes.ReplaceAll(encoded, []byte(" "), nil)
				if !bytes.Contains(compact, []byte(wantJSON)) || bytes.Contains(compact, []byte(`"preprocessing_enabled"`)) {
					t.Fatalf("encoded v6 settings must persist only mode %q: %s", test.want, encoded)
				}
			})
		}
	}
}

func TestSatellitePreprocessingRoundTripsEveryMode(t *testing.T) {
	for _, mode := range []SatellitePreprocessingMode{SatellitePreprocessingDisabled, SatellitePreprocessingShared, SatellitePreprocessingPerSession} {
		t.Run(string(mode), func(t *testing.T) {
			s := Default()
			s.SatellitePreprocessingMode = mode
			s.PreprocessingEnabled = mode != SatellitePreprocessingDisabled
			encoded, err := json.Marshal(documentFromSnapshot(Snapshot{Revision: 1, Settings: s}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := Decode(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if got.Settings.SatellitePreprocessingMode != mode {
				t.Fatalf("round trip mode=%q, want %q", got.Settings.SatellitePreprocessingMode, mode)
			}
		})
	}
}

func TestSatellitePreprocessingV6StrictFieldContract(t *testing.T) {
	valid, err := json.Marshal(documentFromSnapshot(Snapshot{Revision: 1, Settings: Default()}))
	if err != nil {
		t.Fatal(err)
	}
	validText := string(valid)
	if _, err := Decode(strings.NewReader(validText)); err != nil {
		t.Fatalf("valid v6 rejected: %v", err)
	}

	withoutMode := strings.Replace(validText, `"satellite_preprocessing_mode":"shared",`, "", 1)
	legacyOnly := strings.Replace(validText, `"satellite_preprocessing_mode":"shared"`, `"preprocessing_enabled":true`, 1)
	both := strings.Replace(validText, `"satellite_preprocessing_mode":"shared"`, `"preprocessing_enabled":true,"satellite_preprocessing_mode":"shared"`, 1)
	duplicate := strings.Replace(validText, `"satellite_preprocessing_mode":"shared"`, `"satellite_preprocessing_mode":"shared","satellite_preprocessing_mode":"per_session"`, 1)
	wrongType := strings.Replace(validText, `"satellite_preprocessing_mode":"shared"`, `"satellite_preprocessing_mode":true`, 1)
	unknown := strings.Replace(validText, `"satellite_preprocessing_mode":"shared"`, `"satellite_preprocessing_mode":"shared","satellite_preprocessing_scope":"shared"`, 1)
	legacyV5WithNewMode := strings.Replace(both, `"version":6`, `"version":5`, 1)

	for name, document := range map[string]string{
		"missing v6 mode":             withoutMode,
		"legacy field in v6":          legacyOnly,
		"both legacy and v6 fields":   both,
		"duplicate v6 field":          duplicate,
		"wrong v6 field type":         wrongType,
		"unknown field":               unknown,
		"v6 field in legacy document": legacyV5WithNewMode,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(document)); err == nil {
				t.Fatal("invalid settings document accepted")
			}
		})
	}
}

func legacySatelliteSettingsDocument(version int, enabled string) string {
	document := strings.Replace(`{
  "version": VERSION,
  "revision": 7,
  "continue_existing": true,
  "screen_enabled": false,
  "card_detail": "standard",
  "show_technical_actions": true,
  "notify_background_questions": false,
  "notify_background_errors": true,
  "session_lifetime": "12h",
  "queue_limit": 32,
  "voice_recognition": "parakeet",
  "retry_undelivered_files": false,
  "preprocessing_enabled": LEGACY,
  "preprocessing_instruction": ""
}`, "VERSION", strconv.Itoa(version), 1)
	document = strings.Replace(document, "LEGACY", enabled, 1)
	if version >= 2 {
		document = strings.Replace(document, `"card_detail": "standard",`, `"card_detail": "standard", "card_page_limit": 64,`, 1)
	}
	if version >= 3 {
		document = strings.Replace(document, `"preprocessing_enabled":`, `"archive_recommendations": false, "default_providers": {}, "default_workdirs": {}, "preprocessing_enabled":`, 1)
	}
	if version >= 5 {
		document = strings.Replace(document, `"preprocessing_instruction": ""`, `"preprocessing_instruction": "", "session_naming_enabled": false`, 1)
	}
	return document
}
