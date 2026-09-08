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

func TestTechnicalOutputLinesOldVersionsAndReload(t *testing.T) {
	ctx := context.Background()
	for version := 1; version <= 5; version++ {
		for _, choice := range []string{"absent", "5", "10", "20", "40"} {
			t.Run(fmt.Sprintf("v%d/%s", version, choice), func(t *testing.T) {
				document := legacySettingsDocument(version)
				want := 10
				if choice != "absent" {
					document = strings.TrimSuffix(document, "}") + `,"technical_output_lines":` + choice + `}`
					if err := json.Unmarshal([]byte(choice), &want); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(t.TempDir(), "settings.json")
				if err := os.WriteFile(path, []byte(document), 0600); err != nil {
					t.Fatal(err)
				}
				store, err := settings.OpenFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				assertLines := func(s settings.Settings) {
					t.Helper()
					data, err := json.Marshal(s)
					if err != nil {
						t.Fatal(err)
					}
					var got map[string]any
					if err := json.Unmarshal(data, &got); err != nil {
						t.Fatal(err)
					}
					if got["technical_output_lines"] != float64(want) {
						t.Fatalf("technical_output_lines=%v, want %d", got["technical_output_lines"], want)
					}
				}
				before, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				assertLines(before)
				if err := store.Update(ctx, func(s *settings.Settings) error { s.StandbyEnabled = true; return nil }); err != nil {
					t.Fatal(err)
				}
				reopened, err := settings.OpenFileStore(path)
				if err != nil {
					t.Fatal(err)
				}
				after, err := reopened.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				assertLines(after)
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), fmt.Sprintf(`"technical_output_lines": %d`, want)) {
					t.Fatalf("persisted lines missing: %v", err)
				}
				// Reload a synthetic local edit; invalid edits must retain the last good value.
				originalField := fmt.Sprintf(`"technical_output_lines": %d`, want)
				for _, value := range []string{"40", "0", "6", "null", `"10"`, "-1", "41", "10.5"} {
					edit := strings.Replace(string(data), originalField, `"technical_output_lines": `+value, 1)
					if err := os.WriteFile(path, []byte(edit), 0600); err != nil {
						t.Fatal(err)
					}
					_, err := reopened.Reload(ctx)
					if value == "40" {
						if err != nil {
							t.Fatal(err)
						}
						want = 40
					} else if err == nil {
						t.Fatalf("accepted invalid line limit %s", value)
					}
					current, err := reopened.Load(ctx)
					if err != nil {
						t.Fatal(err)
					}
					assertLines(current)
				}
			})
		}
	}
}
