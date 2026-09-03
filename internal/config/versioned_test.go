package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"bria/internal/config"
)

func TestDecodeVersionedCombinedConfiguration(t *testing.T) {
	document := versionedConfigJSON(t, config.RoleCombined, true)
	got, err := config.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.Version != config.CurrentVersion || got.Role != config.RoleCombined || got.Computer == nil ||
		got.Computer.ID != "macbook-main" || got.Network == nil || got.Paths == nil ||
		got.Update == nil || got.Backup == nil || got.Parakeet == nil || got.MediaLimits == nil {
		t.Fatalf("decoded composition = %#v", got)
	}
	if got.IsLegacy() || !got.Coordinates() || !got.Executes() {
		t.Fatalf("role helpers disagree with combined config: %#v", got)
	}
	if got.Backup.Schedule != nil || got.Backup.Encryption != nil {
		t.Fatalf("undecided backup fields must remain explicitly unset: %#v", got.Backup)
	}
}

func TestVersionedCoordinatorAllowsZeroProviders(t *testing.T) {
	document := versionedConfigJSON(t, config.RoleCoordinator, false)
	got, err := config.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(got.Providers) != 0 || !got.Coordinates() || got.Executes() {
		t.Fatalf("coordinator capabilities = %#v", got)
	}
}

func TestVersionedRuntimeFeaturesAreAcceptedOnlyWhenFullyExplicit(t *testing.T) {
	document := withRuntimeFeatures(versionedConfigJSON(t, config.RoleCombined, true))
	got, err := config.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode(runtime features) error = %v", err)
	}
	if got.Version != config.CurrentVersion {
		t.Fatalf("version = %d", got.Version)
	}
	p4, ok := got.P4Runtime()
	if !ok || p4.ArtifactRetryKey.SecretFile != "/var/lib/bria/artifact-retry.key" || len(p4.ArtifactAllowedRoots) != 2 {
		t.Fatalf("P4Runtime() = %#v, %t", p4, ok)
	}
	p4.ArtifactAllowedRoots[0] = "/mutated"
	again, ok := got.P4Runtime()
	if !ok || again.ArtifactAllowedRoots[0] != "/srv/bria-work" {
		t.Fatalf("P4Runtime() did not return defensive roots: %#v", again)
	}
	if _, ok := got.DiscoveryRuntime(); !ok {
		t.Fatal("DiscoveryRuntime() = disabled")
	}
	if _, ok := got.BackupRuntime(); !ok {
		t.Fatal("BackupRuntime() = disabled")
	}
	if _, ok := got.UpdateRuntime(); !ok {
		t.Fatal("UpdateRuntime() = disabled")
	}
}

func TestVersionedRuntimeFeaturesRejectPartialRelativeAndOverlappingPaths(t *testing.T) {
	valid := withRuntimeFeatures(versionedConfigJSON(t, config.RoleCombined, true))
	for name, document := range map[string]string{
		"partial P4":           strings.Replace(valid, `"photo_custody_directory": "/var/lib/bria/media-photos",`, "", 1),
		"relative P4 root":     strings.Replace(valid, `"voice_temp_directory": "/var/lib/bria/media-voice"`, `"voice_temp_directory": "media-voice"`, 1),
		"state overlap":        strings.Replace(valid, `"voice_temp_directory": "/var/lib/bria/media-voice"`, `"voice_temp_directory": "/var/lib/bria/state.json"`, 1),
		"nested artifact root": strings.Replace(valid, `"/srv/bria-work"`, `"/var/lib/bria/artifact-manifests/source"`, 1),
		"inline retry secret":  strings.Replace(valid, `"artifact_retry_key": {"secret_file": "/var/lib/bria/artifact-retry.key"}`, `"artifact_retry_key": {"secret": "forbidden"}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Decode(strings.NewReader(document)); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}
}

func TestVersionedRuntimeFeaturesAreDisabledWhenAbsentAndRoleOwnedWhenPresent(t *testing.T) {
	withoutRuntime, err := config.Decode(strings.NewReader(versionedConfigJSON(t, config.RoleCombined, true)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withoutRuntime.P4Runtime(); ok {
		t.Fatal("P4Runtime() enabled without an explicit block")
	}

	executorP4 := withRuntimeFeatures(versionedConfigJSON(t, config.RoleExecutor, true))
	if _, err := config.Decode(strings.NewReader(executorP4)); err == nil {
		t.Fatal("executor accepted combined-owned runtime blocks")
	}
}

func TestVersionedExecutorHasNoTelegramSecretsOrListener(t *testing.T) {
	document := versionedConfigJSON(t, config.RoleExecutor, true)
	got, err := config.Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got.OwnerUserID != 0 || got.PrivateChatID != 0 || got.BotUsername != "" ||
		got.TelegramToken != (config.TelegramTokenRef{}) || got.CallbackKey != (config.CallbackKeyRef{}) {
		t.Fatalf("executor retained Telegram configuration: %#v", got)
	}
	if got.Network == nil || got.Network.CoordinatorAddress == "" || got.Network.ListenerAddress != "" ||
		got.Coordinates() || !got.Executes() {
		t.Fatalf("executor network/role = %#v", got)
	}
}

func TestVersionedDecodeRejectsRoleViolationsUnknownFieldsAndPathOverlap(t *testing.T) {
	combined := versionedConfigJSON(t, config.RoleCombined, true)
	executor := versionedConfigJSON(t, config.RoleExecutor, true)
	tests := []struct {
		name     string
		document string
	}{
		{"unknown version", strings.Replace(combined, `"version": 1`, `"version": 99`, 1)},
		{"unknown nested field", strings.Replace(combined, `"computer": {`, `"computer": {"secret": "inline",`, 1)},
		{"combined remote coordinator", strings.Replace(combined, `"coordinator_address": ""`, `"coordinator_address": "host.example:443"`, 1)},
		{"coordinator missing listener", strings.Replace(versionedConfigJSON(t, config.RoleCoordinator, false), `"listener_address": "127.0.0.1:7443"`, `"listener_address": ""`, 1)},
		{"executor has listener", strings.Replace(executor, `"listener_address": ""`, `"listener_address": "127.0.0.1:7443"`, 1)},
		{"executor missing coordinator", strings.Replace(executor, `"coordinator_address": "coordinator.example:7443"`, `"coordinator_address": ""`, 1)},
		{"executor credential-like coordinator", strings.Replace(executor, `"coordinator_address": "coordinator.example:7443"`, `"coordinator_address": "user@coordinator.example:7443"`, 1)},
		{"executor telegram field", strings.Replace(executor, `"state_path":`, `"owner_user_id": 42, "state_path":`, 1)},
		{"relative certificate", strings.Replace(combined, `"certificate_file": "/var/lib/bria/tls.crt"`, `"certificate_file": "tls.crt"`, 1)},
		{"path overlap", strings.Replace(combined, `"ledger_path": "/var/lib/bria/ledger.json"`, `"ledger_path": "/var/lib/bria/fence.json"`, 1)},
		{"non https update source", strings.Replace(combined, `https://updates.example/manifest.json`, `http://updates.example/manifest.json`, 1)},
		{"credential query update source", strings.Replace(combined, `https://updates.example/manifest.json`, `https://updates.example/manifest.json?token=secret`, 1)},
		{"backup schedule invented", strings.Replace(combined, `"schedule": null`, `"schedule": "daily"`, 1)},
		{"backup encryption invented", strings.Replace(combined, `"encryption": null`, `"encryption": "aes"`, 1)},
		{"zero media limit", strings.Replace(combined, `"voice_bytes": 10485760`, `"voice_bytes": 0`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := config.Decode(strings.NewReader(test.document)); err == nil {
				t.Fatal("Decode() error = nil, want strict rejection")
			}
		})
	}
}

func TestVersionedDecodeRequiresExactlyOneSeparateParakeetModelPlaceholder(t *testing.T) {
	valid := versionedConfigJSON(t, config.RoleCombined, true)
	for name, document := range map[string]string{
		"missing":   strings.Replace(valid, `"argv": ["{model_path}"]`, `"argv": []`, 1),
		"embedded":  strings.Replace(valid, `"argv": ["{model_path}"]`, `"argv": ["--model={model_path}"]`, 1),
		"duplicate": strings.Replace(valid, `"argv": ["{model_path}"]`, `"argv": ["{model_path}", "{model_path}"]`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Decode(strings.NewReader(document)); err == nil {
				t.Fatal("Decode() error = nil, want strict rejection")
			}
		})
	}
}

func TestVersionedUnknownFieldErrorDoesNotEchoPotentialSecret(t *testing.T) {
	const secretFieldName = "secret-value-leak"
	document := strings.Replace(
		versionedConfigJSON(t, config.RoleCombined, true),
		`"computer": {`,
		`"computer": {"`+secretFieldName+`": true,`,
		1,
	)
	_, err := config.Decode(strings.NewReader(document))
	if err == nil {
		t.Fatal("Decode() error = nil")
	}
	if strings.Contains(err.Error(), secretFieldName) {
		t.Fatalf("Decode() error leaked unknown key: %v", err)
	}
}

func TestLegacyConfigurationRequiresExplicitMigration(t *testing.T) {
	legacy, err := config.Decode(strings.NewReader(validConfigJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.IsLegacy() || legacy.Version != 0 || legacy.EffectiveRole() != config.RoleCombined {
		t.Fatalf("legacy marker = %#v", legacy)
	}
	if _, err := legacy.MigrateLegacy(config.LegacyMigration{}); err == nil {
		t.Fatal("MigrateLegacy(empty) error = nil")
	}
	hybrid := legacy
	hybrid.Computer = &config.ComputerConfig{ID: "silently-added", Name: "Invalid hybrid"}
	if err := hybrid.Validate(); err == nil {
		t.Fatal("legacy configuration accepted versioned fields without explicit migration")
	}

	migrated, err := legacy.MigrateLegacy(versionedMigration(t))
	if err != nil {
		t.Fatalf("MigrateLegacy() error = %v", err)
	}
	if migrated.Version != config.CurrentVersion || migrated.Role != config.RoleCombined || migrated.IsLegacy() {
		t.Fatalf("migrated config = %#v", migrated)
	}
	if legacy.Version != 0 || legacy.Computer != nil {
		t.Fatalf("migration mutated legacy source: %#v", legacy)
	}
}

func TestLegacyMigrationRejectsCollisionWithLoadedConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bria.json")
	if err := os.WriteFile(path, []byte(validConfigJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	migration := versionedMigration(t)
	migration.Backup.Destination = path
	if _, err := legacy.MigrateLegacy(migration); err == nil {
		t.Fatal("MigrateLegacy() accepted config file as backup destination")
	}
}

func versionedConfigJSON(t *testing.T, role config.Role, providers bool) string {
	t.Helper()
	providerDocument := `{}`
	if providers {
		providerDocument = fmt.Sprintf(`{
    "codex": {"enabled": true, "command": {"exec": %s, "argv": []}},
    "claude": {"enabled": false}
  }`, strconv.Quote(validExecutablePath))
	}
	telegram := `
  "owner_user_id": 123456789,
  "private_chat_id": 123456789,
  "bot_username": "@my_bria_bot",
  "telegram_token": {"env_var": "BRIA_TELEGRAM_TOKEN"},
  "callback_key": {"secret_file": "/var/lib/bria/callback.key"},`
	coordinatorAddress := ""
	listenerAddress := "127.0.0.1:7443"
	pairingPath := "/var/lib/bria/pairing.json"
	catalogPath := "/var/lib/bria/catalog.json"
	fencePath := "/var/lib/bria/fence.json"
	ledgerPath := "/var/lib/bria/ledger.json"
	if role == config.RoleExecutor {
		telegram = ""
		coordinatorAddress = "coordinator.example:7443"
		listenerAddress = ""
		pairingPath = ""
		catalogPath = ""
	}
	if role == config.RoleCoordinator {
		fencePath = ""
		ledgerPath = ""
	}
	return fmt.Sprintf(`{
  "version": 1,
  "role": %q,
  "computer": {"id": "macbook-main", "name": "MacBook Main"},%s
  "state_path": "/var/lib/bria/state.json",
  "network": {
    "coordinator_address": %q,
    "listener_address": %q,
    "certificate_file": "/var/lib/bria/tls.crt",
    "private_key_file": "/var/lib/bria/tls.key",
    "trust_bundle_file": "/var/lib/bria/trust.pem"
  },
  "paths": {
    "pairing_path": %q,
    "catalog_path": %q,
    "fence_path": %q,
    "ledger_path": %q
  },
  "providers": %s,
  "update": {"source_url": "https://updates.example/manifest.json", "trust_key_file": "/var/lib/bria/update.pub"},
  "backup": {"destination": "/var/lib/bria-owner-backup", "schedule": null, "encryption": null},
	  "parakeet": {"executable": "/opt/bria-tools/parakeet", "model_path": "/opt/bria-tools/parakeet-model.bin", "argv": ["{model_path}"]},
  "media_limits": {
    "download_bytes": 20971520,
    "upload_bytes": 52428800,
    "voice_bytes": 10485760,
    "photo_bytes": 20971520,
    "transcript_bytes": 1048576,
    "diagnostic_bytes": 65536
  }
}`, role, telegram, coordinatorAddress, listenerAddress, pairingPath, catalogPath, fencePath, ledgerPath, providerDocument)
}

func withRuntimeFeatures(document string) string {
	return strings.TrimSuffix(document, "\n}") + `,
  "runtime": {
    "p4": {
      "voice_temp_directory": "/var/lib/bria/media-voice",
      "photo_custody_directory": "/var/lib/bria/media-photos",
      "artifact_manifest_directory": "/var/lib/bria/artifact-manifests",
      "artifact_retry_directory": "/var/lib/bria/artifact-retries",
      "artifact_allowed_roots": ["/srv/bria-work", "/srv/bria-output"],
      "artifact_retry_key": {"secret_file": "/var/lib/bria/artifact-retry.key"},
      "artifact_retry_ttl_seconds": 3600
    },
    "discovery": {
      "codex_root": "/var/lib/bria/codex-discovery",
      "claude_root": "/var/lib/bria/claude-discovery"
    },
    "backup": {
      "work_directory": "/var/lib/bria/backup-work",
      "latest_path": "/var/lib/bria/backup-latest",
      "restore_candidate_directory": "/var/lib/bria/backup-restore",
      "live_directory": "/var/lib/bria/backup-live",
      "marker_path": "/var/lib/bria/backup.marker",
      "codex_history_root": "/var/lib/bria/codex-history",
      "claude_history_root": "/var/lib/bria/claude-history",
      "harness_root": "/var/lib/bria/harness"
    },
    "update": {
      "stage_directory": "/var/lib/bria/update-stage",
      "state_directory": "/var/lib/bria/update-state",
      "install_root": "/opt/bria",
      "lock_path": "/var/lib/bria/update.lock",
			"verifier_path": "/opt/bria-verify/bria-verify",
      "fingerprint_path": "/var/lib/bria/state.fingerprint",
			"install_executable": "/opt/bria-install/bria-install",
			"rollback_executable": "/opt/bria-install/bria-rollback",
      "config_file": "/etc/bria/runtime.json",
			"health_probe_path": "/opt/bria-health/bria-health"
    }
  }
}`
}

func versionedMigration(t *testing.T) config.LegacyMigration {
	t.Helper()
	return config.LegacyMigration{
		Computer: config.ComputerConfig{ID: "macbook-main", Name: "MacBook Main"},
		Network: config.NetworkConfig{
			ListenerAddress: "127.0.0.1:7443",
			CertificateFile: "/var/lib/bria/tls.crt",
			PrivateKeyFile:  "/var/lib/bria/tls.key",
			TrustBundleFile: "/var/lib/bria/trust.pem",
		},
		Paths: config.RuntimePaths{
			PairingPath: "/var/lib/bria/pairing.json",
			CatalogPath: "/var/lib/bria/catalog.json",
			FencePath:   "/var/lib/bria/fence.json",
			LedgerPath:  "/var/lib/bria/ledger.json",
		},
		Update: config.UpdateConfig{SourceURL: "https://updates.example/manifest.json", TrustKeyFile: "/var/lib/bria/update.pub"},
		Backup: config.BackupConfig{Destination: "/var/lib/bria-owner-backup"},
		Parakeet: config.ParakeetConfig{
			Executable: "/opt/bria/parakeet", ModelPath: "/opt/bria/parakeet-model.bin", Argv: []string{"{model_path}"},
		},
		MediaLimits: config.MediaLimits{
			DownloadBytes: 20 << 20, UploadBytes: 50 << 20, VoiceBytes: 10 << 20,
			PhotoBytes: 20 << 20, TranscriptBytes: 1 << 20, DiagnosticBytes: 64 << 10,
		},
	}
}

func TestVersionedConfigAllPathsAreAbsolute(t *testing.T) {
	got, err := config.Decode(strings.NewReader(withRuntimeFeatures(versionedConfigJSON(t, config.RoleCombined, true))))
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range got.PathReferences() {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s path %q is not absolute", name, path)
		}
	}
}

func TestVersionedFileStorePersistsAndDefensivelyClonesComposition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bria.json")
	if err := os.WriteFile(path, []byte(withRuntimeFeatures(versionedConfigJSON(t, config.RoleCombined, true))), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	initial.Config.Computer.Name = "mutated"
	initial.Config.Parakeet.Argv = append(initial.Config.Parakeet.Argv, "mutated")
	initial.Config.Runtime.P4.ArtifactAllowedRoots[0] = "/mutated"
	current, err := store.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.Config.Computer.Name != "MacBook Main" || len(current.Config.Parakeet.Argv) != 1 ||
		current.Config.Runtime.P4.ArtifactAllowedRoots[0] != "/srv/bria-work" {
		t.Fatalf("Current returned aliased composition: %#v", current.Config)
	}
	committed, err := store.SetProviderEnabled(context.Background(), current.Revision, "codex", false)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Config.Version != config.CurrentVersion || committed.Config.Role != config.RoleCombined {
		t.Fatalf("CAS lost versioned composition: %#v", committed.Config)
	}
	persisted, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ProviderEnabled("codex") || persisted.Computer == nil || persisted.Computer.ID != "macbook-main" {
		t.Fatalf("persisted composition = %#v", persisted)
	}
}
