package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

func TestRecoverClusterCAPreservesStateIdentityAndDropsStalePeers(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	dataDir := filepath.Join(directory, "data")
	address := availableTestAddress(t)
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "android", "--node-name", "Android",
		"--data-dir", dataDir, "--config", configPath,
		"--raft-bind", address, "--raft-advertise", address,
	}); err != nil {
		t.Fatal(err)
	}
	source, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	oldCA := source.CACertificate
	oldCertificatePEM, err := os.ReadFile(source.NodeCertificate)
	if err != nil {
		t.Fatal(err)
	}
	oldCertificate, err := security.ParseCertificate(oldCertificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint, err := security.NodeCertificateFingerprint(oldCertificate)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := writeCARecoveryTestBackup(t, source, oldCertificatePEM, oldFingerprint)

	source.Bootstrap = false
	source.CAPrivateKey = ""
	source.EnrollmentIssuerID = ""
	source.RaftPeers = []config.RaftPeer{
		{NodeID: "android", NodeName: "Android", Address: address},
		{NodeID: "stale", NodeName: "Stale", Address: "127.0.0.1:29999"},
	}
	if err := writeConfigAtomic(configPath, source); err != nil {
		t.Fatal(err)
	}
	if err := recoverClusterCA([]string{
		"--config", configPath, "--backup", backupPath, "--confirm", "personal",
	}); err != nil {
		t.Fatal(err)
	}

	recovered, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Bootstrap || recovered.CAPrivateKey == "" ||
		recovered.EnrollmentIssuerID != "android" || recovered.CACertificate == oldCA {
		t.Fatalf("recovered config did not activate a replacement bootstrap identity: %#v", recovered)
	}
	if len(recovered.RaftPeers) != 1 || recovered.RaftPeers[0].NodeID != "android" {
		t.Fatalf("stale peers survived recovery: %#v", recovered.RaftPeers)
	}
	if err := verifyRecoveredIdentity(recovered, oldFingerprint); err != nil {
		t.Fatal(err)
	}
	newCertificatePEM, err := os.ReadFile(recovered.NodeCertificate)
	if err != nil {
		t.Fatal(err)
	}
	newCertificate, err := security.ParseCertificate(newCertificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	newFingerprint, err := security.NodeCertificateFingerprint(newCertificate)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "android", Name: "Android", Lifecycle: domain.NodeActive,
		Fingerprint: oldFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.PublishNodeHeartbeat(
		"android", "boot", "test", "linux", "arm64", newFingerprint, oldFingerprint,
		nil, nil, nil, nil, time.Now().UTC(),
	); err != nil {
		t.Fatalf("replicated state rejected linked replacement identity: %v", err)
	}
	if state.Nodes["android"].Fingerprint != newFingerprint {
		t.Fatal("heartbeat did not pin the replacement identity")
	}
}

func availableTestAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func writeCARecoveryTestBackup(
	t *testing.T,
	nodeConfig config.Config,
	certificatePEM []byte,
	fingerprint string,
) string {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: domain.NodeID(nodeConfig.NodeID), Name: nodeConfig.NodeName,
		Status: domain.NodeOnline, Lifecycle: domain.NodeActive, Fingerprint: fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	snapshot, err := machine.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	backup, err := clusterbackup.New(
		nodeConfig.ClusterID, nodeConfig.NodeID, snapshot, time.Now().UTC(),
	)
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
	path := filepath.Join(t.TempDir(), "backup.json")
	if err := writeExclusive(path, encoded); err != nil {
		t.Fatal(err)
	}
	return path
}
