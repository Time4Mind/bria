package settings_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"bria/internal/settings"
)

func TestHiddenDirectoriesDecodePreference(t *testing.T) {
	document := strings.TrimSuffix(legacySettingsDocument(5), "}") + `,"show_hidden_directories":true}`
	snapshot, err := settings.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode explicit hidden-directory preference: %v", err)
	}
	encoded, err := json.Marshal(snapshot.Settings)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"show_hidden_directories":true`)) {
		t.Fatal("decoded settings lost the explicit hidden-directory preference")
	}
}

func TestHiddenDirectoriesDefaultsAndLegacyDocumentsStayOff(t *testing.T) {
	if settings.Default().ShowHiddenDirectories || settings.Default().Effective().ShowHiddenDirectories {
		t.Fatal("hidden directories must default to OFF")
	}
	for version := 1; version <= settings.FormatVersion; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			legacy := legacySettingsDocument(version)
			before, err := settings.Decode(strings.NewReader(legacy))
			if err != nil || before.Settings.ShowHiddenDirectories || before.Settings.Effective().ShowHiddenDirectories {
				t.Fatalf("missing-field legacy decode=%+v err=%v", before, err)
			}
			for _, want := range []bool{false, true} {
				document := strings.TrimSuffix(legacy, "}") + fmt.Sprintf(`,"show_hidden_directories":%t}`, want)
				got, err := settings.Decode(strings.NewReader(document))
				if err != nil || got.Settings.ShowHiddenDirectories != want || got.Settings.Effective().ShowHiddenDirectories != want {
					t.Fatalf("explicit preference decode=%+v err=%v", got, err)
				}
				got.Settings.ShowHiddenDirectories = false
				if !reflect.DeepEqual(got, before) {
					t.Fatal("hidden-directory preference changed unrelated legacy settings")
				}
			}
		})
	}
}

func TestHiddenDirectoriesCodecStillRejectsMalformedFields(t *testing.T) {
	for _, extra := range []string{
		`"show_hidden_directories":"true"`,
		`"show_hidden_directories":1`,
		`"show_hidden_directories":true,"show_hidden_directories":false`,
		`"show_hidden_directories":true,"unknown_preference":false`,
	} {
		document := strings.TrimSuffix(legacySettingsDocument(5), "}") + "," + extra + "}"
		if _, err := settings.Decode(strings.NewReader(document)); err == nil {
			t.Fatalf("malformed settings accepted: %s", extra)
		}
	}
}
