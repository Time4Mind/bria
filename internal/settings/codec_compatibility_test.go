package settings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/settings"
)

func legacySettingsDocument(version int) string {
	document := fmt.Sprintf(`{"version":%d,"revision":7,
"continue_existing":false,"screen_enabled":true,"card_detail":"compact",
"show_technical_actions":false,"notify_background_questions":true,
"notify_background_errors":false,"session_lifetime":"48h","queue_limit":41,
"voice_recognition":"parakeet","retry_undelivered_files":true`, version)
	if version >= 2 {
		document += `,"card_page_limit":128`
	}
	if version >= 3 {
		document += `,"archive_recommendations":true,"default_providers":{"local":"claude"},"default_workdirs":{"local":"/workspace"}`
	}
	if version >= 4 {
		document += `,"preprocessing_enabled":true,"preprocessing_instruction":"clean speech"`
	}
	if version >= 5 {
		document += `,"session_naming_enabled":true`
	}
	return document + `}`
}

func TestCodecCompatibilityMigratesEveryVersionAndPersistsApprovalChoice(t *testing.T) {
	for version := 1; version <= 5; version++ {
		for _, choice := range []string{"absent", "false", "true"} {
			t.Run(fmt.Sprintf("v%d/%s", version, choice), func(t *testing.T) {
				document := legacySettingsDocument(version)
				if choice != "absent" {
					document = strings.TrimSuffix(document, "}") + `,"auto_approve_commands":` + choice + `}`
				}
				path := filepath.Join(t.TempDir(), "settings.json")
				if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
					t.Fatal(err)
				}
				store, err := settings.OpenFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				before, err := store.Current(context.Background())
				if err != nil || before.Revision != 7 || before.Settings.Version != 5 {
					t.Fatalf("migration = %+v, %v", before, err)
				}
				want := settings.Settings{
					Version: 5, ContinueExisting: false, ScreenEnabled: true, ScreenCaptureLimitKiB: 48,
					CardDetail: settings.CardDetailCompact, CardPageLimit: 64, ShowTechnicalActions: false,
					NotifyBackgroundQuestions: true, NotifyBackgroundErrors: false, SessionLifetime: settings.Lifetime48Hours,
					QueueLimit: 41, VoiceRecognition: settings.VoiceParakeet, RetryUndeliveredFiles: true,
					DefaultProviders: map[string]string{}, DefaultWorkdirs: map[string]string{}, AutoApproveCommands: choice != "false",
				}
				if version >= 2 {
					want.CardPageLimit = 128
				}
				if version >= 3 {
					want.ArchiveRecommendations = true
					want.DefaultProviders["local"] = "claude"
					want.DefaultWorkdirs["local"] = "/workspace"
				}
				if version >= 4 {
					want.PreprocessingEnabled = true
					want.PreprocessingInstruction = "clean speech"
				}
				want.SessionNamingEnabled = version >= 5
				assertSettings := func(got settings.Settings) {
					t.Helper()
					gotJSON, _ := json.Marshal(got)
					wantJSON, _ := json.Marshal(want)
					if !bytes.Equal(gotJSON, wantJSON) {
						t.Fatalf("settings = %s, want %s", gotJSON, wantJSON)
					}
				}
				assertSettings(before.Settings)
				// An unrelated owner edit must persist the migrated choice, including explicit OFF.
				if err := store.Update(context.Background(), func(s *settings.Settings) error {
					s.StandbyEnabled = true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				want.StandbyEnabled = true
				reopened, err := settings.OpenFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				after, err := reopened.Current(context.Background())
				if err != nil || after.Revision != 8 {
					t.Fatalf("persisted revision = %d, %v", after.Revision, err)
				}
				assertSettings(after.Settings)
				data, err := os.ReadFile(path)
				if err != nil || !bytes.Contains(data, []byte(fmt.Sprintf(`"auto_approve_commands": %t`, want.AutoApproveCommands))) {
					t.Fatalf("approval choice missing on disk: %v", err)
				}
			})
		}
	}
}

func TestCodecCompatibilityRejectsMalformedApprovalDocuments(t *testing.T) {
	base := strings.TrimSuffix(legacySettingsDocument(5), "}")
	for name, document := range map[string]string{
		"duplicate approval": base + `,"auto_approve_commands":false,"auto_approve_commands":true}`,
		"escaped duplicate":  base + `,"auto_approve_commands":false,"auto_approve_\u0063ommands":true}`,
		"wrong type":         base + `,"auto_approve_commands":"false"}`,
		"unknown field":      base + `,"auto_approve_command":false}`,
		"trailing value":     base + `} true`,
		"invalid UTF8":       base + ",\"preference\":\"\xff\"}",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := settings.Decode(strings.NewReader(document)); err == nil {
				t.Fatal("malformed settings accepted")
			}
		})
	}
}
