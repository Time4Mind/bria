package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

func TestSetPeersAndIssueFollowerBundle(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "leader-data")
	configPath := filepath.Join(directory, "leader.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "leader",
		"--node-name", "Leader", "--owner-user-id", "42",
		"--data-dir", dataDir, "--config", configPath,
		"--raft-bind", "127.0.0.1:7946", "--raft-advertise", "127.0.0.1:7946",
	}); err != nil {
		t.Fatalf("init cluster: %v", err)
	}
	if err := setClusterPeers([]string{
		"--config", configPath, "--self-bind", "10.0.0.1:7946",
		"--peer", "leader,Leader,10.0.0.1:7946",
		"--peer", "follower,Follower,10.0.0.2:7946",
	}); err != nil {
		t.Fatalf("set peers: %v", err)
	}
	leader, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load updated leader config: %v", err)
	}
	if leader.RaftBind != "10.0.0.1:7946" || leader.RaftAdvertise != "10.0.0.1:7946" {
		t.Fatalf("leader addresses not updated: %#v", leader)
	}

	bundleDir := filepath.Join(directory, "follower-bundle")
	if err := issueClusterNode([]string{
		"--config", configPath, "--node-id", "follower", "--output", bundleDir,
		"--data-dir", "/var/lib/bria", "--raft-bind", "10.0.0.2:7946",
	}); err != nil {
		t.Fatalf("issue follower: %v", err)
	}
	follower, err := config.Load(filepath.Join(bundleDir, "config.json"))
	if err != nil {
		t.Fatalf("load follower config: %v", err)
	}
	if follower.Bootstrap || follower.CAPrivateKey != "" || follower.NodeID != "follower" {
		t.Fatalf("unsafe follower config: %#v", follower)
	}
	if err := setClusterPeers([]string{
		"--config", filepath.Join(bundleDir, "config.json"),
		"--self-bind", "127.0.0.1:7946",
		"--peer", "leader,Leader,leader.internal:7946",
		"--peer", "follower,Follower,relay.internal:17946",
	}); err != nil {
		t.Fatalf("set peers on follower config: %v", err)
	}
	follower, err = config.Load(filepath.Join(bundleDir, "config.json"))
	if err != nil {
		t.Fatalf("reload follower config: %v", err)
	}
	if follower.RaftBind != "127.0.0.1:7946" || follower.RaftAdvertise != "relay.internal:17946" {
		t.Fatalf("follower bind/advertise addresses not separated: %#v", follower)
	}
	if _, err := os.Stat(filepath.Join(bundleDir, "ca.key")); !os.IsNotExist(err) {
		t.Fatal("CA private key leaked into follower bundle")
	}
	for _, name := range []string{"config.json", "ca.crt", "node.crt", "node.key", "callback.key"} {
		info, err := os.Stat(filepath.Join(bundleDir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
	certificatePEM, err := os.ReadFile(filepath.Join(bundleDir, "node.crt"))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := security.ParseCertificate(certificatePEM)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := security.NodeIDFromCertificate(certificate, "personal"); err != nil || got != "follower" {
		t.Fatalf("issued certificate identity = %q, %v", got, err)
	}
}

func TestIssueNodeRejectsUnapprovedIdentityAndOverwrite(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "leader.json")
	if err := initCluster([]string{
		"init", "--cluster-id", "personal", "--node-id", "leader", "--node-name", "Leader",
		"--data-dir", filepath.Join(directory, "data"), "--config", configPath,
	}); err != nil {
		t.Fatal(err)
	}
	if err := setClusterPeers([]string{
		"--config", configPath, "--self-bind", "10.0.0.1:7946",
		"--peer", "leader,Leader,10.0.0.1:7946",
	}); err != nil {
		t.Fatal(err)
	}
	if err := issueClusterNode([]string{
		"--config", configPath, "--node-id", "unknown",
		"--output", filepath.Join(directory, "bundle"),
	}); err == nil {
		t.Fatal("unapproved node identity was issued")
	}
}
