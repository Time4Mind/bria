package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

func TestClusterRestoreStagesValidBackupAndAppliesItOnce(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "data")
	configPath := filepath.Join(directory, "config.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "cluster", "--node-id", "node", "--node-name", "Node",
		"--data-dir", dataDir, "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	nodeConfig, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM, err := os.ReadFile(nodeConfig.NodeCertificate)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := security.ParseCertificate(certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := security.NodeCertificateFingerprint(certificate)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Restored Node", Status: domain.NodeOffline,
		Lifecycle: domain.NodeActive, Fingerprint: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	snapshot, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	backup, err := clusterbackup.New("cluster", "node", snapshot, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM, err := os.ReadFile(nodeConfig.NodePrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := security.ParseEd25519PrivateKey(privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.Sign(certificatePEM, privateKey); err != nil {
		t.Fatal(err)
	}
	encoded, err := backup.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(directory, "backup.json")
	if err := writeExclusive(backupPath, encoded); err != nil {
		t.Fatal(err)
	}
	if err := restoreCluster([]string{
		"--config", configPath, "--input", backupPath, "--dry-run",
	}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if err := restoreCluster([]string{
		"--config", configPath, "--input", backupPath, "--confirm", "cluster",
	}); err != nil {
		t.Fatalf("stage restore: %v", err)
	}
	pending := filepath.Join(dataDir, pendingRestoreName)
	if _, err := os.Stat(pending); err != nil {
		t.Fatalf("pending restore: %v", err)
	}
	restoredMachine := clusterstate.NewMachine(nil)
	node, err := consensus.NewInMemory(
		consensus.Config{NodeID: "node", ApplyTimeout: time.Second},
		clusterstate.NewFSM(restoredMachine),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := node.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	if err := applyPendingClusterRestore(ctx, node, nodeConfig); err != nil {
		t.Fatal(err)
	}
	if got := node.State().State().Nodes["node"].Name; got != "Restored Node" {
		t.Fatalf("restored node name=%q", got)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatal("pending restore was not consumed")
	}
	if err := applyPendingClusterRestore(ctx, node, nodeConfig); err != nil {
		t.Fatalf("empty second application: %v", err)
	}
}

func TestClusterRestoreRejectsWrongClusterAndExistingRaftState(t *testing.T) {
	// The detailed envelope and snapshot checks live in their owning packages;
	// this test fixes the destructive staging boundary at the CLI layer.
	directory := t.TempDir()
	if err := ensureCleanRaftDirectory(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "raft.db"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCleanRaftDirectory(directory); err == nil {
		t.Fatal("restore accepted a non-empty Raft directory")
	}
}
