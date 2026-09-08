package settingscomposition_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/settingscomposition"
	"bria/internal/settingsport"
)

func TestProviderPreferencesCompatibilityRenamesOnlyLocalDisplayName(t *testing.T) {
	ctx := context.Background()
	legacy, err := config.Decode(strings.NewReader(providerConfigFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configuration, err := legacy.MigrateLegacy(config.LegacyMigration{
		Computer: config.ComputerConfig{ID: "local", Name: "Original"},
		Paths: config.RuntimePaths{
			PairingPath: filepath.Join(dir, "pairing.json"), CatalogPath: filepath.Join(dir, "catalog.json"),
			FencePath: filepath.Join(dir, "fence.json"), LedgerPath: filepath.Join(dir, "ledger.json"),
		},
		Update:      config.UpdateConfig{SourceURL: "https://updates.example/manifest.json", TrustKeyFile: filepath.Join(dir, "update.pub")},
		Parakeet:    config.ParakeetConfig{Executable: "/opt/parakeet", ModelPath: "/opt/parakeet-model", Argv: []string{"{model_path}"}},
		MediaLimits: config.MediaLimits{DownloadBytes: 1024, UploadBytes: 1024, VoiceBytes: 1024, PhotoBytes: 1024, TranscriptBytes: 1024, DiagnosticBytes: 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bria.json")
	if err := os.WriteFile(path, document, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prefs := settingscomposition.ProviderPreferences{Store: store}
	if err := prefs.RenameNode(ctx, "local", "  Рабочий компьютер  "); err != nil {
		t.Fatal(err)
	}
	reopened, err := config.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := initial.Config
	computer := *want.Computer
	computer.Name = "Рабочий компьютер"
	want.Computer = &computer
	if !reflect.DeepEqual(got.Config, want) {
		t.Fatal("rename changed configuration beyond the local display name")
	}
	capabilities, err := prefs.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(capabilities, []settingsport.ProviderPreference{
		{Provider: domain.ProviderCodex, Enabled: true, Configured: true},
		{Provider: domain.ProviderClaude, Enabled: false, Configured: false},
	}) {
		t.Fatalf("capabilities = %+v, %v", capabilities, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ node, name string }{
		{"other", "New name"}, {"local", " \t "}, {"local", strings.Repeat("я", 65)},
	} {
		if err := prefs.RenameNode(ctx, domain.ComputerID(invalid.node), invalid.name); err == nil {
			t.Fatal("invalid rename accepted")
		}
	}
	if err := prefs.ToggleProvider(ctx, domain.ProviderClaude); err == nil {
		t.Fatal("unconfigured provider enabled")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected operations changed configuration: %v", err)
	}
}

func TestProviderPreferencesCompatibilityRequiresStore(t *testing.T) {
	prefs := settingscomposition.ProviderPreferences{}
	ctx := context.Background()
	if _, err := prefs.Snapshot(ctx); err == nil {
		t.Fatal("snapshot without store accepted")
	}
	if err := prefs.ToggleProvider(ctx, domain.ProviderCodex); err == nil {
		t.Fatal("toggle without store accepted")
	}
	if err := prefs.RenameNode(ctx, "local", "Name"); err == nil {
		t.Fatal("rename without store accepted")
	}
}
