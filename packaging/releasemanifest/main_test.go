package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bria/internal/update"
)

func TestSignedReleaseManifestRoundTripAndTamperRejection(t *testing.T) {
	t.Parallel()
	releaseDir, privatePath, trustPath := releaseFixture(t, "1.2.3", "primary")
	if err := signRelease(releaseDir, "1.2.3", "primary", privatePath, trustPath); err != nil {
		t.Fatalf("signRelease(): %v", err)
	}
	if err := verifyRelease(releaseDir, "1.2.3", trustPath); err != nil {
		t.Fatalf("verifyRelease(): %v", err)
	}

	artifact := filepath.Join(releaseDir, "bria_1.2.3_linux_amd64.tar.gz")
	file, err := os.OpenFile(artifact, os.O_APPEND|os.O_WRONLY, 0)
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
	if err := verifyRelease(releaseDir, "1.2.3", trustPath); !errors.Is(err, update.ErrArtifactHash) {
		t.Fatalf("tampered verify error = %v, want ErrArtifactHash", err)
	}
}

func TestVerifyArtifactAcceptsOnlyTheExactSelectedSignedArtifact(t *testing.T) {
	t.Parallel()
	releaseDir, privatePath, trustPath := releaseFixture(t, "1.2.3", "primary")
	if err := signRelease(releaseDir, "1.2.3", "primary", privatePath, trustPath); err != nil {
		t.Fatalf("signRelease(): %v", err)
	}
	artifact := filepath.Join(releaseDir, "bria_1.2.3_linux_amd64.tar.gz")
	manifest := filepath.Join(releaseDir, signedManifestName)
	if err := verifySelectedArtifact(manifest, artifact, "1.2.3", "linux", "amd64", trustPath); err != nil {
		t.Fatalf("verifySelectedArtifact(): %v", err)
	}
	if err := verifySelectedArtifact(manifest, artifact, "1.2.3", "darwin", "amd64", trustPath); !errors.Is(err, update.ErrInvalidManifest) {
		t.Fatalf("platform mismatch error = %v, want ErrInvalidManifest", err)
	}
}

func TestSigningRequiresTrustedMatchingKey(t *testing.T) {
	t.Parallel()
	releaseDir, privatePath, trustPath := releaseFixture(t, "1.2.3", "primary")
	_, unrelatedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateKey(t, privatePath, unrelatedPrivate)
	if err := signRelease(releaseDir, "1.2.3", "primary", privatePath, trustPath); !errors.Is(err, update.ErrInvalidSignature) {
		t.Fatalf("mismatched signing key error = %v, want ErrInvalidSignature", err)
	}
}

func TestPrivateKeyErrorsRedactPathAndRejectLoosePermissions(t *testing.T) {
	t.Parallel()
	secretPath := filepath.Join(t.TempDir(), "do-not-leak-private-key-name.pem")
	if _, err := loadPrivateKey(secretPath); err == nil || strings.Contains(err.Error(), secretPath) {
		t.Fatalf("private key error leaked path: %v", err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPrivateKey(secretPath); err == nil {
		t.Fatal("loadPrivateKey accepted mode 0644")
	}
}

func TestSigningKeyCannotBePassedInProcessArguments(t *testing.T) {
	t.Parallel()
	err := run([]string{
		"sign",
		"-release-dir", "/release",
		"-version", "1.2.3",
		"-key-id", "primary",
		"-trust-file", "/trust.json",
		"-private-key-file", "/do-not-expose-key-location",
	})
	if err == nil || err.Error() != "invalid command arguments" {
		t.Fatalf("private key argv error = %v, want invalid command arguments", err)
	}
}

func releaseFixture(t *testing.T, version, keyID string) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	releaseDir := filepath.Join(root, version)
	if err := os.Mkdir(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		name := "bria_" + version + "_" + target + ".tar.gz"
		if err := os.WriteFile(filepath.Join(releaseDir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "private.pem")
	writePrivateKey(t, privatePath, privateKey)
	trustPath := filepath.Join(root, "trusted-keys.json")
	document := trustDocument{FormatVersion: 1, Keys: []trustKey{{KeyID: keyID, PublicKey: base64.StdEncoding.EncodeToString(publicKey)}}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath, encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	return releaseDir, privatePath, trustPath
}

func writePrivateKey(t *testing.T, path string, privateKey ed25519.PrivateKey) {
	t.Helper()
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
		t.Fatal(err)
	}
}
