package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPackageBundlesIsDeterministicAndWritesManifest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	bundle := filepath.Join(input, "bria_1.2.3_linux_amd64")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(bundle, "bria"), []byte("binary"), 0o755)
	writeTestFile(t, filepath.Join(bundle, "release.json"), []byte("{}\n"), 0o644)

	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	timestamp := time.Unix(1_700_000_000, 0).UTC()
	if err := packageBundles(input, first, timestamp); err != nil {
		t.Fatal(err)
	}
	if err := packageBundles(input, second, timestamp); err != nil {
		t.Fatal(err)
	}

	archive := "bria_1.2.3_linux_amd64.tar.gz"
	firstArchive := readTestFile(t, filepath.Join(first, archive))
	secondArchive := readTestFile(t, filepath.Join(second, archive))
	if !bytes.Equal(firstArchive, secondArchive) {
		t.Fatal("archives differ despite identical inputs and SOURCE_DATE_EPOCH")
	}
	manifest := readTestFile(t, filepath.Join(first, "SHA256SUMS"))
	if !bytes.Contains(manifest, []byte("  "+archive+"\n")) {
		t.Fatalf("manifest does not name archive: %q", manifest)
	}
}

func TestPackageBundlesRejectsSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bundle := filepath.Join(root, "input", "bundle")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	writeTestFile(t, target, []byte("sensitive"), 0o600)
	if err := os.Symlink(target, filepath.Join(bundle, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := packageBundles(filepath.Dir(bundle), filepath.Join(root, "output"), time.Unix(0, 0)); err == nil {
		t.Fatal("packageBundles accepted a symlink")
	}
}

func TestReleaseVerifierAcceptsCompleteMatrixAndRejectsTampering(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	version := "1.2.3-test"
	for _, target := range []struct {
		os, arch string
	}{
		{os: "darwin", arch: "amd64"},
		{os: "darwin", arch: "arm64"},
		{os: "linux", arch: "amd64"},
		{os: "linux", arch: "arm64"},
	} {
		bundle := filepath.Join(input, "bria_"+version+"_"+target.os+"_"+target.arch)
		if err := os.MkdirAll(bundle, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{
			"bria", "bria-codex-adapter", "bria-claude-adapter", "bria-releasemanifest", "render-service.sh", "validate-install.sh",
			"install-release.sh", "rollback-install.sh", "service-control.sh", "postflight.sh",
		} {
			writeTestFile(t, filepath.Join(bundle, name), []byte(name), 0o755)
		}
		metadata := []byte(`{"version":"` + version + `","revision":"abc123","os":"` + target.os + `","arch":"` + target.arch + `"}` + "\n")
		writeTestFile(t, filepath.Join(bundle, "release.json"), metadata, 0o644)
		if target.os == "darwin" {
			writeTestFile(t, filepath.Join(bundle, "com.time4mind.bria.plist.tmpl"), []byte("plist"), 0o644)
		} else {
			writeTestFile(t, filepath.Join(bundle, "bria.service.tmpl"), []byte("linux"), 0o644)
			writeTestFile(t, filepath.Join(bundle, "bria-wsl.service.tmpl"), []byte("wsl"), 0o644)
		}
	}
	output := filepath.Join(root, version)
	if err := packageBundles(input, output, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	privateKeyPath, trustPath := writeSigningFixture(t, root)
	sign := exec.Command("go", "run", "../releasemanifest", "sign",
		"-release-dir", output,
		"-version", version,
		"-key-id", "test-primary",
		"-trust-file", trustPath,
	)
	sign.Env = append(os.Environ(), "RELEASE_SIGNING_KEY_FILE="+privateKeyPath)
	if result, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("sign release: %v\n%s", err, result)
	}
	verify := exec.Command("../verify-release.sh", output)
	verify.Env = append(os.Environ(), "RELEASE_TRUST_FILE="+trustPath)
	if result, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("complete release rejected: %v\n%s", err, result)
	}
	if err := os.WriteFile(filepath.Join(output, "SHA256SUMS"), []byte("legacy checksum index is not authoritative\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verify = exec.Command("../verify-release.sh", output)
	verify.Env = append(os.Environ(), "RELEASE_TRUST_FILE="+trustPath)
	if result, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("signed manifest was not authoritative: %v\n%s", err, result)
	}

	tampered := filepath.Join(output, "bria_"+version+"_linux_amd64.tar.gz")
	file, err := os.OpenFile(tampered, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	verify = exec.Command("../verify-release.sh", output)
	verify.Env = append(os.Environ(), "RELEASE_TRUST_FILE="+trustPath)
	if result, err := verify.CombinedOutput(); err == nil {
		t.Fatalf("tampered release accepted:\n%s", result)
	}
}

func writeSigningFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encodedPrivateKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPath := filepath.Join(root, "release-signing-key.pem")
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedPrivateKey}), 0o600); err != nil {
		t.Fatal(err)
	}
	trustPath := filepath.Join(root, "release-trust.json")
	trustDocument := struct {
		FormatVersion int `json:"format_version"`
		Keys          []struct {
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		} `json:"keys"`
	}{FormatVersion: 1}
	trustDocument.Keys = append(trustDocument.Keys, struct {
		KeyID     string `json:"key_id"`
		PublicKey string `json:"public_key"`
	}{KeyID: "test-primary", PublicKey: base64.StdEncoding.EncodeToString(publicKey)})
	encodedTrust, err := json.Marshal(trustDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, encodedTrust, 0o644); err != nil {
		t.Fatal(err)
	}
	return privateKeyPath, trustPath
}

func writeTestFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
