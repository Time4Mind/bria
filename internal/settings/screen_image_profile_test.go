package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenImageProfileDefaultsAndLegacyDocumentsUseFull8(t *testing.T) {
	if profile := Default().ScreenImageProfile; profile != ScreenImageProfileFull8 {
		t.Fatalf("default screen image profile = %q, want full_8", profile)
	}

	legacy := legacyScreenImageSettingsDocument(t)
	snapshot, err := Decode(bytes.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if profile := snapshot.Settings.ScreenImageProfile; profile != ScreenImageProfileFull8 {
		t.Fatalf("legacy screen image profile = %q, want full_8", profile)
	}
}

func TestScreenImageProfileDecodesAndPersistsExplicitChoice(t *testing.T) {
	for _, value := range []string{"current", "full_8", "compact_8"} {
		t.Run(value, func(t *testing.T) {
			document := legacyScreenImageSettingsDocument(t)
			document = bytes.TrimSuffix(document, []byte("}"))
			document = append(document, []byte(`,"screen_image_profile":"`+value+`"}`)...)
			snapshot, err := Decode(bytes.NewReader(document))
			if err != nil {
				t.Fatalf("decode %q: %v", value, err)
			}
			if profile := snapshot.Settings.ScreenImageProfile; string(profile) != value {
				t.Fatalf("decoded profile = %q, want %q", profile, value)
			}
		})
	}
}

func TestScreenImageProfileRejectsUnknownChoice(t *testing.T) {
	document := bytes.TrimSuffix(legacyScreenImageSettingsDocument(t), []byte("}"))
	document = append(document, []byte(`,"screen_image_profile":"other"}`)...)
	if _, err := Decode(bytes.NewReader(document)); err == nil {
		t.Fatal("unknown screen image profile accepted")
	}
}

func TestScreenImageProfilePersistsEveryExplicitChoiceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []ScreenImageProfile{ScreenImageProfileCurrent, ScreenImageProfileFull8, ScreenImageProfileCompact8} {
		if err := store.Update(context.Background(), func(current *Settings) error {
			current.ScreenImageProfile = profile
			return nil
		}); err != nil {
			t.Fatalf("persist %q: %v", profile, err)
		}
		reopened, err := OpenFileStore(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.Load(context.Background())
		if err != nil || got.ScreenImageProfile != profile {
			t.Fatalf("reopen profile = %q, want %q (err=%v)", got.ScreenImageProfile, profile, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte(`"screen_image_profile": "`+profile+`"`)) {
			t.Fatalf("profile %q missing on disk: err=%v data=%s", profile, err, data)
		}
		store = reopened
	}
}

func legacyScreenImageSettingsDocument(t *testing.T) []byte {
	t.Helper()
	encoded, err := json.Marshal(documentFromSnapshot(Snapshot{Revision: 1, Settings: Default()}))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "screen_image_profile")
	encoded, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "screen_image_profile") {
		t.Fatal("legacy fixture unexpectedly contains screen image profile")
	}
	return encoded
}
