package telegramcontroller_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/settingsport"
	"bria/internal/telegramcontroller"
)

func TestDirectoryBrowserHidesDotDirectoriesBeforePagination(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(fmt.Sprint(local), func(t *testing.T) {
			root := t.TempDir()
			var children []sessioncreation.Directory
			for _, name := range []string{".cache", ".git", "..hidden", ".config", ".a", ".b", ".c", ".d", "project.v2", "visible"} {
				path := filepath.Join(root, name)
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				children = append(children, sessioncreation.Directory{Name: name, Path: path})
			}
			if err := os.WriteFile(filepath.Join(root, "regular-file"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			capabilities := []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}
			var environment sessioncreation.Environment = &creationEnvironmentStub{
				computers: []sessioncreation.Computer{{ID: "local", Name: "Test node", Capabilities: capabilities}},
				roots:     map[domain.ComputerID][]sessioncreation.Directory{"local": {{Name: root, Path: root}}},
				children:  map[string][]sessioncreation.Directory{root: children},
			}
			if local {
				environment = localCreationEnvironment(t, root, capabilities...)
			}
			// One page: hidden entries must not consume the eight available slots.
			prefs := &testPreferences{settings: settingsport.Snapshot{CardPageLimit: 1}}
			c := newController(t, nil, newLockedSessions(), nil, nil, telegramcontroller.Options{Settings: prefs, CreationEnvironment: environment})
			t.Cleanup(func() { _ = c.Close(context.Background()) })
			result, err := c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew, UpdateID: 1})
			if err != nil || result.Surface == nil {
				t.Fatalf("browser result=%#v error=%v", result, err)
			}
			var labels []string
			for _, row := range result.Surface.Rows {
				for _, button := range row {
					if button.Action == telegramcontroller.SemanticCreateChoice {
						labels = append(labels, button.Label)
					}
				}
			}
			joined := strings.Join(labels, "|")
			if len(labels) != 2 || !strings.Contains(joined, "project.v2") || !strings.Contains(joined, "visible") {
				t.Fatalf("directory choices=%q, want only project.v2 and visible", labels)
			}
			if hasSemanticAction(result.Surface.Rows, telegramcontroller.SemanticCreateNext) {
				t.Fatal("hidden entries created extra page")
			}
		})
	}
}

func TestDirectoryBrowserHiddenRootRemainsAccessibleWithoutSettings(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".explicit-root")
	if err := os.MkdirAll(filepath.Join(root, "visible"), 0700); err != nil {
		t.Fatal(err)
	}
	c := newController(t, nil, newLockedSessions(), nil, nil, telegramcontroller.Options{
		CreationEnvironment: localCreationEnvironment(t, root, sessioncreation.ProviderCapability{Provider: domain.ProviderCodex, Installed: true, Enabled: true}),
	})
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	result, err := c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew})
	if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, root) {
		t.Fatalf("explicit hidden root unavailable: %#v, %v", result, err)
	}
	for _, row := range result.Surface.Rows {
		for _, button := range row {
			if button.Action == telegramcontroller.SemanticCreateChoice && button.Label == "📁 visible" {
				return
			}
		}
	}
	t.Fatal("visible directory inside hidden root was hidden")
}
