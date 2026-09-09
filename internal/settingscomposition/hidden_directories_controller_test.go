package settingscomposition_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/settings"
	"bria/internal/settingscomposition"
	"bria/internal/telegramcontroller"
)

func TestDirectoryBrowserTogglePersistsThroughPublicController(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, name := range []string{".git", "project.v2"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	for phase, wantHidden := range []bool{false, true, false} {
		store, err := settings.OpenFileStore(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		environment, err := sessioncreation.NewLocalEnvironment("local", "Local", []string{root}, func(context.Context) ([]sessioncreation.ProviderCapability, error) {
			return []sessioncreation.ProviderCapability{{Provider: domain.ProviderCodex, Installed: true, Enabled: true}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		c, err := telegramcontroller.New(42, 42, "local", settingsControllerCreator{}, settingsControllerSessions{}, settingsControllerSubmit{}, settingsControllerNotifier{}, telegramcontroller.Options{
			Settings:            settingscomposition.Preferences{Store: store},
			CreationEnvironment: environment,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close(ctx) })
		result, err := c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuNew})
		if err != nil || result.Surface == nil {
			t.Fatalf("phase=%d browser: %v", phase, err)
		}
		labels := ""
		for _, row := range result.Surface.Rows {
			for _, button := range row {
				labels += button.Label + "\n"
			}
		}
		if strings.Contains(labels, "📁 .git") != wantHidden || !strings.Contains(labels, "📁 project.v2") {
			t.Fatalf("phase=%d persisted hidden=%v labels=%s", phase, wantHidden, labels)
		}
		if phase < 2 {
			result, err = c.HandleSemanticAction(ctx, telegramcontroller.SemanticAction{Kind: "settings_hidden_directories"})
			if err != nil || result.Surface == nil || !strings.Contains(result.Surface.Text, "Скрытые каталоги") {
				t.Fatalf("toggle phase=%d result=%#v err=%v", phase, result, err)
			}
		}
		if err := c.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
