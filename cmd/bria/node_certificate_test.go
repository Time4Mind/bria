package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

func TestNodeCertificateRenewalInstallAndRollback(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "data")
	configPath := filepath.Join(directory, "config.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "alpha", "--node-name", "Alpha",
		"--data-dir", dataDir, "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	original, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(directory, "request.json")
	statePath := filepath.Join(directory, "state.json")
	responsePath := filepath.Join(directory, "response.json")
	if err := createCertificateRenewalRequest([]string{
		"--config", configPath, "--request", requestPath, "--state", statePath,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{requestPath, statePath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	if err := renewClusterNodeCertificate([]string{
		"--config", configPath, "--request", requestPath, "--response", responsePath,
		"--confirm-node-id", "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	if err := installCertificateRenewal([]string{
		"--config", configPath, "--state", statePath, "--response", responsePath,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatal("private renewal state remains after successful install")
	}
	renewed, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.NodeCertificate == original.NodeCertificate || renewed.NodePrivateKey == original.NodePrivateKey {
		t.Fatal("config did not switch to versioned renewal files")
	}
	if err := verifyCredentialPair(renewed, renewed.NodeCertificate, renewed.NodePrivateKey); err != nil {
		t.Fatalf("renewed pair: %v", err)
	}
	if err := rollbackCertificateRenewal([]string{"--config", configPath}); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.NodeCertificate != original.NodeCertificate ||
		rolledBack.NodePrivateKey != original.NodePrivateKey {
		t.Fatal("rollback did not restore original credential paths")
	}
}

func TestNodeCertificateRenewalFailureKeepsConfigAndPrivateState(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "alpha", "--node-name", "Alpha",
		"--data-dir", filepath.Join(directory, "data"), "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(directory, "request.json")
	statePath := filepath.Join(directory, "state.json")
	responsePath := filepath.Join(directory, "response.json")
	if err := createCertificateRenewalRequest([]string{
		"--config", configPath, "--request", requestPath, "--state", statePath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renewClusterNodeCertificate([]string{
		"--config", configPath, "--request", requestPath, "--response", responsePath,
		"--confirm-node-id", "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	response, err := os.ReadFile(responsePath)
	if err != nil {
		t.Fatal(err)
	}
	response[len(response)/2] ^= 1
	if err := os.WriteFile(responsePath, response, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installCertificateRenewal([]string{
		"--config", configPath, "--state", statePath, "--response", responsePath,
	}); err == nil {
		t.Fatal("tampered response was installed")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("failed install changed config")
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatal("failed install removed private renewal state")
	}
}

func TestNodeCertificateRenewalRejectsUnsafeStatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no POSIX mode bits")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadCertificateRenewalState(path); err == nil {
		t.Fatal("unsafe private renewal state permissions were accepted")
	}
}

func TestNodeCertificateRenewalRetriesPublishedCredentialStaging(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "alpha", "--node-name", "Alpha",
		"--data-dir", filepath.Join(directory, "data"), "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(directory, "request.json")
	statePath := filepath.Join(directory, "state.json")
	responsePath := filepath.Join(directory, "response.json")
	if err := createCertificateRenewalRequest([]string{
		"--config", configPath, "--request", requestPath, "--state", statePath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := renewClusterNodeCertificate([]string{
		"--config", configPath, "--request", requestPath, "--response", responsePath,
		"--confirm-node-id", "alpha",
	}); err != nil {
		t.Fatal(err)
	}
	nodeConfig, _ := config.Load(configPath)
	state, privateKey, err := loadCertificateRenewalState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var response security.CertificateRenewalResponse
	if err := readBoundedJSON(responsePath, 128<<10, &response); err != nil {
		t.Fatal(err)
	}
	privateKeyPEM, _ := security.MarshalEd25519PrivateKey(privateKey)
	previous := previousNodeCredentials{
		Certificate: nodeConfig.NodeCertificate, PrivateKey: nodeConfig.NodePrivateKey,
		RecordedAt: state.Request.CreatedAt,
	}
	if _, err := installCredentialFiles(nodeConfig, response, privateKeyPEM, previous); err != nil {
		t.Fatal(err)
	}
	if err := installCertificateRenewal([]string{
		"--config", configPath, "--state", statePath, "--response", responsePath,
	}); err != nil {
		t.Fatalf("retry staged install: %v", err)
	}
}

func TestClusterCertificateRenewalRequiresExactNodeConfirmation(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "alpha", "--node-name", "Alpha",
		"--data-dir", filepath.Join(directory, "data"), "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(directory, "request.json")
	statePath := filepath.Join(directory, "state.json")
	if err := createCertificateRenewalRequest([]string{
		"--config", configPath, "--request", requestPath, "--state", statePath,
	}); err != nil {
		t.Fatal(err)
	}
	responsePath := filepath.Join(directory, "response.json")
	if err := renewClusterNodeCertificate([]string{
		"--config", configPath, "--request", requestPath, "--response", responsePath,
		"--confirm-node-id", "beta",
	}); err == nil {
		t.Fatal("wrong node confirmation was accepted")
	}
	if _, err := os.Stat(responsePath); !os.IsNotExist(err) {
		t.Fatal("wrong confirmation produced a response")
	}
}
