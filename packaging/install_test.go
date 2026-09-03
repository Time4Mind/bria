package packaging_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallUpgradeAndRollbackKeepOnlyManagedPointers(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	first := signedReleaseFixture(t, root, "1.0.0", "1.0.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, first, trustPath, installRoot, configPath)
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
	if _, err := os.Lstat(filepath.Join(installRoot, "previous")); !os.IsNotExist(err) {
		t.Fatalf("previous after clean install exists: %v", err)
	}

	second := signedReleaseFixture(t, root, "1.1.0", "1.1.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, second, trustPath, installRoot, configPath)
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.1.0")
	assertLink(t, filepath.Join(installRoot, "previous"), "releases/1.0.0")

	if err := os.WriteFile(filepath.Join(installRoot, "releases", "1.0.0", "bria"), []byte("#!/bin/sh\nprintf 'bria compromised\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runScript(t, "rollback-install.sh", installRoot, configPath, trustPath)
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
	assertLink(t, filepath.Join(installRoot, "previous"), "releases/1.1.0")
	command := exec.Command(filepath.Join(installRoot, "current", "bria"), "--version")
	result, err := command.CombinedOutput()
	if err != nil || string(result) != "bria 1.0.0\n" {
		t.Fatalf("rollback did not reconstruct signed previous bundle: %v, %q", err, result)
	}
}

func TestSelectedSignedArtifactInstallAndRollbackDoNotRequireOtherArchives(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := filepath.Join(root, "bria-releasemanifest")
	build := exec.Command("go", "build", "-o", verifier, "./releasemanifest")
	build.Env = offlineGoEnv()
	if result, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build verifier: %v\n%s", err, result)
	}

	first := selectedReleaseFixture(t, root, signedReleaseFixture(t, root, "1.0.0", "1.0.0", trustPath, privatePath, false), "1.0.0")
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, "1.0.0", first, trustPath, installRoot, configPath, verifier, "install-op-1", "node", "0.9.0", "state-v1")
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
	runScript(t, "rollback-install.sh", installRoot, configPath, trustPath, verifier, "rollback-noop", "node", "2.0.0", "1.0.0", "state-v1")
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
	assertFileContent(t, filepath.Join(installRoot, "update-operation-receipts", "rollback-noop"), "rollback-noop\nnode\n2.0.0\n1.0.0\nstate-v1\n")

	second := selectedReleaseFixture(t, root, signedReleaseFixture(t, root, "1.1.0", "1.1.0", trustPath, privatePath, false), "1.1.0")
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, "1.1.0", second, trustPath, installRoot, configPath, verifier, "install-op-2", "node", "1.0.0", "state-v1")
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.1.0")
	assertFileContent(t, filepath.Join(installRoot, "update-operation-receipts", "install-op-2"), "install-op-2\nnode\n1.0.0\n1.1.0\nstate-v1\n")

	runScript(t, "rollback-install.sh", installRoot, configPath, trustPath, verifier, "rollback-op-2", "node", "1.1.0", "1.0.0", "state-v1")
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
	assertFileContent(t, filepath.Join(installRoot, "update-operation-receipts", "rollback-op-2"), "rollback-op-2\nnode\n1.1.0\n1.0.0\nstate-v1\n")
	receiptDirectory := filepath.Join(installRoot, "release-receipts", "1.0.0")
	archive := filepath.Join(receiptDirectory, "bria_1.0.0_"+nativeArtifactOS()+"_"+runtime.GOARCH+".tar.gz")
	verifyArguments := []string{"verify-installed",
		"-manifest", filepath.Join(receiptDirectory, "release-manifest.json"), "-artifact", archive,
		"-installed", filepath.Join(installRoot, "releases", "1.0.0"), "-version", "1.0.0",
		"-platform", nativeArtifactOS(), "-arch", runtime.GOARCH, "-trust-file", trustPath}
	verifyInstalled := exec.Command(verifier, verifyArguments...)
	if result, err := verifyInstalled.CombinedOutput(); err != nil {
		t.Fatalf("verify installed: %v\n%s", err, result)
	}
	installedBinary := filepath.Join(installRoot, "releases", "1.0.0", "bria")
	file, err := os.OpenFile(installedBinary, os.O_APPEND|os.O_WRONLY, 0)
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
	if result, err := exec.Command(verifier, verifyArguments...).CombinedOutput(); err == nil {
		t.Fatalf("tampered installed bundle accepted:\n%s", result)
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("%s = %q, want %q", path, content, expected)
	}
}

func selectedReleaseFixture(t *testing.T, root, fullRelease, version string) string {
	t.Helper()
	selected := filepath.Join(root, "selected-"+version)
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, filepath.Join(fullRelease, "release-manifest.json"), filepath.Join(selected, "release-manifest.json"))
	name := "bria_" + version + "_" + nativeArtifactOS() + "_" + runtime.GOARCH + ".tar.gz"
	copyFixture(t, filepath.Join(fullRelease, name), filepath.Join(selected, name))
	return selected
}

func TestTamperedRollbackReceiptDoesNotMoveCurrent(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := signedReleaseFixture(t, root, "1.0.0", "1.0.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, first, trustPath, installRoot, configPath)
	second := signedReleaseFixture(t, root, "1.1.0", "1.1.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, second, trustPath, installRoot, configPath)
	receiptArchive := filepath.Join(installRoot, "release-receipts", "1.0.0", "bria_1.0.0_"+nativeArtifactOS()+"_"+runtime.GOARCH+".tar.gz")
	file, err := os.OpenFile(receiptArchive, os.O_APPEND|os.O_WRONLY, 0)
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
	command := scriptCommand("rollback-install.sh", installRoot, configPath, trustPath)
	if result, err := command.CombinedOutput(); err == nil {
		t.Fatalf("tampered rollback receipt accepted:\n%s", result)
	}
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.1.0")
}

func TestInstallResumesAfterReceiptWasPersistedBeforeCandidate(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.MkdirAll(filepath.Join(installRoot, "release-receipts", "1.0.0"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := signedReleaseFixture(t, root, "1.0.0", "1.0.0", trustPath, privatePath, false)
	entries, err := os.ReadDir(release)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != "release-manifest.json" && filepath.Ext(entry.Name()) != ".gz") {
			continue
		}
		copyFixture(t, filepath.Join(release, entry.Name()), filepath.Join(installRoot, "release-receipts", "1.0.0", entry.Name()))
	}
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, release, trustPath, installRoot, configPath)
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
}

func TestRollbackCompletesInterruptedPointerSwitch(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := signedReleaseFixture(t, root, "1.0.0", "1.0.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, first, trustPath, installRoot, configPath)
	second := signedReleaseFixture(t, root, "1.1.0", "1.1.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, second, trustPath, installRoot, configPath)
	if err := os.WriteFile(filepath.Join(installRoot, ".rollback-transaction"), []byte("releases/1.1.0\nreleases/1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(installRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/1.0.0", filepath.Join(installRoot, "current")); err != nil {
		t.Fatal(err)
	}
	runScript(t, "rollback-install.sh", installRoot, configPath, trustPath)
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
	assertLink(t, filepath.Join(installRoot, "previous"), "releases/1.1.0")
	if _, err := os.Lstat(filepath.Join(installRoot, ".rollback-transaction")); !os.IsNotExist(err) {
		t.Fatalf("recovered transaction still exists: %v", err)
	}
}

func TestFailedCandidatePostflightDoesNotMoveCurrent(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := signedReleaseFixture(t, root, "1.0.0", "1.0.0", trustPath, privatePath, false)
	runScript(t, "install-release.sh", testPlatform(), runtime.GOARCH, good, trustPath, installRoot, configPath)

	broken := signedReleaseFixture(t, root, "1.1.0-broken", "wrong-version", trustPath, privatePath, false)
	command := scriptCommand("install-release.sh", testPlatform(), runtime.GOARCH, broken, trustPath, installRoot, configPath)
	if result, err := command.CombinedOutput(); err == nil {
		t.Fatalf("broken candidate installed:\n%s", result)
	}
	assertLink(t, filepath.Join(installRoot, "current"), "releases/1.0.0")
}

func TestSignedArchiveCannotInstallSymlink(t *testing.T) {
	root := t.TempDir()
	trustPath, privatePath := signingFixture(t, root)
	installRoot := filepath.Join(root, "install")
	if err := os.Mkdir(installRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := signedReleaseFixture(t, root, "1.0.0-link", "1.0.0-link", trustPath, privatePath, true)
	command := scriptCommand("install-release.sh", testPlatform(), runtime.GOARCH, release, trustPath, installRoot, configPath)
	if result, err := command.CombinedOutput(); err == nil {
		t.Fatalf("signed archive symlink installed:\n%s", result)
	}
	if _, err := os.Lstat(filepath.Join(installRoot, "current")); !os.IsNotExist(err) {
		t.Fatalf("rejected symlink release changed current: %v", err)
	}
}

func signedReleaseFixture(t *testing.T, root, version, binaryVersion, trustPath, privatePath string, linkNativeAdapter bool) string {
	t.Helper()
	releaseDir := filepath.Join(root, version)
	if err := os.Mkdir(releaseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ os, arch string }{
		{os: "darwin", arch: "amd64"}, {os: "darwin", arch: "arm64"},
		{os: "linux", arch: "amd64"}, {os: "linux", arch: "arm64"},
	} {
		bundleName := "bria_" + version + "_" + target.os + "_" + target.arch
		bundle := filepath.Join(root, "bundle-"+bundleName, bundleName)
		if err := os.MkdirAll(bundle, 0o700); err != nil {
			t.Fatal(err)
		}
		bria := []byte("#!/bin/sh\ncase \"${1:-}\" in --version) printf 'bria " + binaryVersion + "\\n' ;; check-config) exit 0 ;; *) exit 0 ;; esac\n")
		writeExecutable(t, filepath.Join(bundle, "bria"), bria)
		writeExecutable(t, filepath.Join(bundle, "bria-codex-adapter"), []byte("#!/bin/sh\nexit 0\n"))
		writeExecutable(t, filepath.Join(bundle, "bria-claude-adapter"), []byte("#!/bin/sh\nexit 0\n"))
		writeExecutable(t, filepath.Join(bundle, "bria-releasemanifest"), []byte("#!/bin/sh\nexit 0\n"))
		if linkNativeAdapter && target.os == nativeArtifactOS() && target.arch == runtime.GOARCH {
			if err := os.Remove(filepath.Join(bundle, "bria-codex-adapter")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("bria", filepath.Join(bundle, "bria-codex-adapter")); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{"render-service.sh", "validate-install.sh", "install-release.sh", "rollback-install.sh", "service-control.sh", "postflight.sh"} {
			content, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(bundle, name), content)
		}
		metadata := []byte(`{"version":"` + version + `","revision":"test","os":"` + target.os + `","arch":"` + target.arch + `","source_date_epoch":0}` + "\n")
		if err := os.WriteFile(filepath.Join(bundle, "release.json"), metadata, 0o644); err != nil {
			t.Fatal(err)
		}
		if target.os == "darwin" {
			copyFixture(t, "macos/com.time4mind.bria.plist.tmpl", filepath.Join(bundle, "com.time4mind.bria.plist.tmpl"))
		} else {
			copyFixture(t, "linux/bria.service.tmpl", filepath.Join(bundle, "bria.service.tmpl"))
			copyFixture(t, "wsl/bria.service.tmpl", filepath.Join(bundle, "bria-wsl.service.tmpl"))
		}
		archive := filepath.Join(releaseDir, bundleName+".tar.gz")
		command := exec.Command("tar", "-czf", archive, "-C", filepath.Dir(bundle), bundleName)
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("create fixture archive: %v\n%s", err, result)
		}
	}
	command := exec.Command("go", "run", "./releasemanifest", "sign",
		"-release-dir", releaseDir, "-version", version, "-key-id", "test-primary",
		"-trust-file", trustPath)
	command.Env = append(offlineGoEnv(), "RELEASE_SIGNING_KEY_FILE="+privatePath)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sign fixture: %v\n%s", err, result)
	}
	return releaseDir
}

func signingFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(root, "private.pem")
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	document := struct {
		FormatVersion int `json:"format_version"`
		Keys          []struct {
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		} `json:"keys"`
	}{FormatVersion: 1}
	document.Keys = append(document.Keys, struct {
		KeyID     string `json:"key_id"`
		PublicKey string `json:"public_key"`
	}{KeyID: "test-primary", PublicKey: base64.StdEncoding.EncodeToString(publicKey)})
	trust, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	trustPath := filepath.Join(root, "trust.json")
	if err := os.WriteFile(trustPath, trust, 0o644); err != nil {
		t.Fatal(err)
	}
	return trustPath, privatePath
}

func runScript(t *testing.T, name string, arguments ...string) {
	t.Helper()
	command := scriptCommand(name, arguments...)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, result)
	}
}

func scriptCommand(name string, arguments ...string) *exec.Cmd {
	command := exec.Command("./"+name, arguments...)
	command.Env = offlineGoEnv()
	return command
}

func offlineGoEnv() []string {
	return append(os.Environ(), "GOENV=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0")
}

func writeExecutable(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, source, destination string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertLink(t *testing.T, path, expected string) {
	t.Helper()
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if target != expected {
		t.Fatalf("%s -> %q, want %q", path, target, expected)
	}
}

func testPlatform() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return "linux"
}

func nativeArtifactOS() string {
	if runtime.GOOS == "darwin" {
		return "darwin"
	}
	return "linux"
}
