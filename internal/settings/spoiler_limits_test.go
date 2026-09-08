package settings_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/settings"
)

func assertSpoilerLimits(t *testing.T, got settings.Settings, command, output int) {
	t.Helper()
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["technical_command_lines"] != float64(command) || fields["technical_output_lines"] != float64(output) {
		t.Fatalf("command/output=%v/%v want=%d/%d", fields["technical_command_lines"], fields["technical_output_lines"], command, output)
	}
}

func TestSpoilerDefaultsAndLegacyMigrationPreserveUnrelatedSettings(t *testing.T) {
	assertSpoilerLimits(t, settings.Default(), 10, 10)
	ctx := context.Background()
	for version := 1; version <= 5; version++ {
		for _, old := range []int{0, 5, 10, 20, 40} {
			t.Run(fmt.Sprintf("v%d/shared%d", version, old), func(t *testing.T) {
				doc := legacySettingsDocument(version)
				if old != 0 {
					doc = strings.TrimSuffix(doc, "}") + fmt.Sprintf(`,"technical_output_lines":%d}`, old)
				}
				path := filepath.Join(t.TempDir(), "settings.json")
				if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
					t.Fatal(err)
				}
				store, err := settings.OpenFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				before, err := store.Current(ctx)
				if err != nil || before.Revision != 7 || before.Settings.QueueLimit != 41 || before.Settings.SessionLifetime != settings.Lifetime48Hours {
					t.Fatal("migration changed unrelated persisted settings or revision")
				}
				want := old
				if old == 0 {
					want = 10
				}
				if old == 40 {
					want = 20
				}
				assertSpoilerLimits(t, before.Settings, want, want)
				if raw, err := os.ReadFile(path); err != nil || string(raw) != doc {
					t.Fatal("read-only migration rewrote the legacy document")
				}
				if err := store.Update(ctx, func(s *settings.Settings) error { s.StandbyEnabled = true; return nil }); err != nil {
					t.Fatal(err)
				}
				reopened, err := settings.OpenFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				after, err := reopened.Current(ctx)
				if err != nil || after.Revision != 8 || !after.Settings.StandbyEnabled {
					t.Fatal("migrated settings were not persisted by the owner update")
				}
				assertSpoilerLimits(t, after.Settings, want, want)
			})
		}
	}
}

func TestSpoilerNewDocumentsAcceptIndependentLimitsAndRejectLegacyForty(t *testing.T) {
	for _, command := range []string{"3", "5", "10", "20", "40", "0", "6", "null", `"10"`, "-1", "10.5"} {
		for _, output := range []string{"3", "5", "10", "20", "40", "0", "6", "null"} {
			t.Run(command+"/"+output, func(t *testing.T) {
				doc := strings.TrimSuffix(legacySettingsDocument(5), "}") + `,"technical_command_lines":` + command + `,"technical_output_lines":` + output + `}`
				got, err := settings.Decode(strings.NewReader(doc))
				valid := func(s string) bool { return s == "3" || s == "5" || s == "10" || s == "20" }
				if !valid(command) || !valid(output) {
					if err == nil {
						t.Fatal("new invalid limits silently migrated or accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var wantCommand, wantOutput int
				_ = json.Unmarshal([]byte(command), &wantCommand)
				_ = json.Unmarshal([]byte(output), &wantOutput)
				assertSpoilerLimits(t, got.Settings, wantCommand, wantOutput)
			})
		}
	}
}

func TestSpoilerCommandReloadRejectsInvalidEditAndRetainsLastGoodPair(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := settings.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, func(s *settings.Settings) error {
		s.TechnicalCommandLines, s.TechnicalOutputLines = 3, 20
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"40", "0", "null", `"5"`, "5.5", "-1"} {
		edited := strings.Replace(string(baseline), `"technical_command_lines": 3`, `"technical_command_lines": `+value, 1)
		if edited == string(baseline) {
			t.Fatal("test did not edit the persisted command field")
		}
		if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Reload(ctx); err == nil {
			t.Fatalf("invalid command edit %s accepted", value)
		}
		got, err := store.Current(ctx)
		if err != nil || got.Revision != 1 {
			t.Fatal("invalid reload changed last-good revision")
		}
		assertSpoilerLimits(t, got.Settings, 3, 20)
		if _, err := settings.OpenFileStore(path); err == nil {
			t.Fatal("new store silently migrated an invalid current document")
		}
	}
}
