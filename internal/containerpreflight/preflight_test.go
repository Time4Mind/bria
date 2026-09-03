package containerpreflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyAcceptsExactExecutorArtifacts(t *testing.T) {
	fixture := newFixture(t, "executor")
	if err := Verify(context.Background(), fixture.options); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestVerifyRejectsRoleMismatchBeforeArtifactProbe(t *testing.T) {
	fixture := newFixture(t, "executor")
	fixture.options.ExpectedRole = "combined"
	if err := Verify(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("Verify() error = %v, want role mismatch", err)
	}
}

func TestVerifyRejectsChangedArtifactBytes(t *testing.T) {
	fixture := newFixture(t, "executor")
	if err := os.Chmod(fixture.codex, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.codex, []byte("#!/bin/sh\nprintf '%s\\n' 'codex 9.9.9'\n"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.codex, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("Verify() error = %v, want integrity failure", err)
	}
}

func TestVerifyRejectsVersionMismatchAfterIntegrityMatches(t *testing.T) {
	fixture := newFixture(t, "executor")
	document, err := os.ReadFile(fixture.lock)
	if err != nil {
		t.Fatal(err)
	}
	document = []byte(strings.Replace(string(document), `"exact_stdout":"codex 1.2.3\n"`, `"exact_stdout":"codex 7.7.7\n"`, 1))
	if err := os.Chmod(fixture.lock, 0o600); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, fixture.lock, document, 0o400)
	if err := Verify(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("Verify() error = %v, want version failure", err)
	}
}

func TestVerifyRejectsSymlinkedArtifact(t *testing.T) {
	fixture := newFixture(t, "executor")
	target := fixture.codex + ".real"
	providerRoot := filepath.Dir(fixture.codex)
	if err := os.Chmod(providerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.codex, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, fixture.codex); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(providerRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), fixture.options); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Verify() error = %v, want symlink failure", err)
	}
}

func TestVerifyCoordinatorNeedsNoProviderArtifacts(t *testing.T) {
	fixture := newFixture(t, "coordinator")
	if err := Verify(context.Background(), fixture.options); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

type preflightFixture struct {
	options Options
	lock    string
	codex   string
}

func newFixture(t *testing.T, role string) preflightFixture {
	t.Helper()
	root := t.TempDir()
	providerRoot := filepath.Join(root, "providers")
	modelRoot := filepath.Join(root, "models")
	stateRoot := filepath.Join(root, "state")
	t.Cleanup(func() {
		_ = os.Chmod(providerRoot, 0o700)
		_ = os.Chmod(modelRoot, 0o700)
	})
	for _, directory := range []string{providerRoot, modelRoot, stateRoot} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	codex := filepath.Join(providerRoot, "codex")
	parakeet := filepath.Join(providerRoot, "parakeet")
	model := filepath.Join(modelRoot, "parakeet-model.bin")
	writePrivate(t, codex, []byte("#!/bin/sh\nprintf '%s\\n' 'codex 1.2.3'\n"), 0o555)
	writePrivate(t, parakeet, []byte("#!/bin/sh\nprintf '%s\\n' 'parakeet 2.0.0'\n"), 0o555)
	writePrivate(t, model, []byte("model-v3"), 0o444)
	if err := os.Chmod(providerRoot, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(modelRoot, 0o555); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(root, "config.json")
	configDocument := versionedConfig(t, root, role, codex, parakeet, model)
	writePrivate(t, configPath, []byte(configDocument), 0o600)

	lockPath := filepath.Join(root, "provider-lock.json")
	artifacts := "[]"
	if role != "coordinator" {
		artifacts = fmt.Sprintf(`[
    %s,
    %s,
    %s
  ]`, executableLock("codex", codex, "codex 1.2.3\n"), executableLock("parakeet", parakeet, "parakeet 2.0.0\n"), dataLock("parakeet_model", model, "parakeet-tdt-0.6b-v3"))
	}
	writePrivate(t, lockPath, []byte(fmt.Sprintf(`{"schema_version":1,"role":%q,"artifacts":%s}`, role, artifacts)), 0o400)
	return preflightFixture{
		options: Options{ConfigPath: configPath, LockPath: lockPath, ExpectedRole: role, ProviderRoot: providerRoot, ModelRoot: modelRoot},
		lock:    lockPath, codex: codex,
	}
}

func versionedConfig(t *testing.T, root, role, codex, parakeet, model string) string {
	t.Helper()
	coordinatorAddress, listenerAddress, telegram := "coordinator.example:7443", "", ""
	pairing, catalog := "", ""
	fence, ledger := filepath.Join(root, "state/fence.json"), filepath.Join(root, "state/ledger.json")
	providers := fmt.Sprintf(`{"codex":{"enabled":true,"command":{"exec":%q,"argv":["app-server"]}}}`, codex)
	if role == "coordinator" {
		coordinatorAddress, listenerAddress = "", "127.0.0.1:7443"
		pairing, catalog = filepath.Join(root, "state/pairing.json"), filepath.Join(root, "state/catalog.json")
		fence, ledger, providers = "", "", "{}"
		token := filepath.Join(root, "state/token")
		callback := filepath.Join(root, "state/callback")
		writePrivate(t, token, []byte("telegram-token-value\n"), 0o600)
		writePrivate(t, callback, []byte(strings.Repeat("k", 32)), 0o600)
		telegram = fmt.Sprintf(`"owner_user_id":42,"private_chat_id":42,"bot_username":"my_bria_bot","telegram_token":{"secret_file":%q},"callback_key":{"secret_file":%q},`, token, callback)
	}
	return fmt.Sprintf(`{
  "version":1,"role":%q,"computer":{"id":"node-1","name":"Node 1"},%s
  "state_path":%q,
  "network":{"coordinator_address":%q,"listener_address":%q,"certificate_file":%q,"private_key_file":%q,"trust_bundle_file":%q},
  "paths":{"pairing_path":%q,"catalog_path":%q,"fence_path":%q,"ledger_path":%q},
  "providers":%s,
  "update":{"source_url":"https://updates.example/release.json","trust_key_file":%q},
  "backup":{"destination":%q,"schedule":null,"encryption":null},
  "parakeet":{"executable":%q,"model_path":%q,"argv":["{model_path}"]},
  "media_limits":{"download_bytes":20971520,"upload_bytes":52428800,"voice_bytes":10485760,"photo_bytes":20971520,"transcript_bytes":1048576,"diagnostic_bytes":65536}
}`, role, telegram, filepath.Join(root, "state/state.json"), coordinatorAddress, listenerAddress,
		filepath.Join(root, "state/tls.crt"), filepath.Join(root, "state/tls.key"), filepath.Join(root, "state/trust.pem"),
		pairing, catalog, fence, ledger, providers, filepath.Join(root, "state/update.pub"), filepath.Join(root, "state/backup"), parakeet, model)
}

func executableLock(name, path, version string) string {
	contents, _ := os.ReadFile(path)
	digest := sha256.Sum256(contents)
	return fmt.Sprintf(`{"name":%q,"kind":"executable","path":%q,"size":%d,"sha256":%q,"identity":%q,"version_probe":{"argv":["--version"],"exact_stdout":%q}}`,
		name, path, len(contents), hex.EncodeToString(digest[:]), name+"-package", version)
}

func dataLock(name, path, identity string) string {
	contents, _ := os.ReadFile(path)
	digest := sha256.Sum256(contents)
	return fmt.Sprintf(`{"name":%q,"kind":"data","path":%q,"size":%d,"sha256":%q,"identity":%q}`, name, path, len(contents), hex.EncodeToString(digest[:]), identity)
}

func writePrivate(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
