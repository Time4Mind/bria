package main

import (
	"path/filepath"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
)

func TestSetClusterOwnerRequiresExactConfirmation(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "cluster", "--node-id", "node", "--node-name", "Node",
		"--owner-user-id", "7", "--data-dir", directory, "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := setClusterOwner([]string{
		"--config", configPath, "--user-id", "9", "--confirm", "8",
	}); err == nil {
		t.Fatal("mismatched confirmation accepted")
	}
	if err := setClusterOwner([]string{
		"--config", configPath, "--user-id", "9", "--confirm", "9",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BootstrapOwnerID != 9 {
		t.Fatalf("owner=%d", loaded.BootstrapOwnerID)
	}
}
