//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessChaosBackupFollowsCurrentLeader(t *testing.T) {
	if os.Getenv(processChaosEnvironment) != "1" {
		t.Skip("set BRIA_PROCESS_CHAOS=1 to run process-level chaos")
	}
	root := t.TempDir()
	prependFakeTmux(t, root)
	configs, certificates, roots := writeChaosCluster(t, root, 3)
	processes := make(map[string]*chaosProcess, len(configs))
	for _, item := range configs {
		processes[item.NodeID] = startChaosNode(t, root, item)
	}
	t.Cleanup(func() {
		for _, process := range processes {
			process.stop(t)
		}
	})
	client := chaosProbeClient(t, configs, certificates["node-1"], roots)
	leaderID := waitForSingleProcessLeader(t, client, configs, 35*time.Second)
	follower := configs[0]
	for _, item := range configs {
		if item.NodeID != leaderID {
			follower = item
			break
		}
	}
	backupPath := filepath.Join(root, "follower-requested-backup.json")
	if err := backupCluster([]string{
		"--config", filepath.Join(follower.DataDir, "config.json"), "--output", backupPath,
	}); err != nil {
		t.Fatalf("follow backup leader from %s to %s: %v", follower.NodeID, leaderID, err)
	}
	backup, _, _, err := loadRestoreCandidate(backupPath, configByID(t, configs, leaderID))
	if err != nil {
		t.Fatalf("verify follower-requested backup: %v", err)
	}
	if backup.SourceNodeID != leaderID {
		t.Fatalf("backup source=%s, want current leader %s", backup.SourceNodeID, leaderID)
	}
}

func TestProcessChaosLogicalBackupRestore(t *testing.T) {
	if os.Getenv(processChaosEnvironment) != "1" {
		t.Skip("set BRIA_PROCESS_CHAOS=1 to run process-level chaos")
	}
	root := t.TempDir()
	prependFakeTmux(t, root)
	configs, certificates, roots := writeChaosCluster(t, root, 1)
	item := configs[0]
	process := startChaosNode(t, root, item)
	t.Cleanup(func() { process.stop(t) })
	client := chaosProbeClient(t, configs, certificates[item.NodeID], roots)
	waitForAllReady(t, client, configs, 25*time.Second)

	configPath := filepath.Join(item.DataDir, "config.json")
	backupPath := filepath.Join(root, "cluster-backup.json")
	if err := backupCluster([]string{
		"--config", configPath, "--target", item.NodeID, "--output", backupPath,
	}); err != nil {
		t.Fatalf("create live backup: %v", err)
	}
	if err := restoreCluster([]string{
		"--config", configPath, "--input", backupPath, "--dry-run",
	}); err != nil {
		t.Fatalf("validate live backup: %v", err)
	}

	process.stop(t)
	raftPath := filepath.Join(item.DataDir, "raft")
	retainedRaftPath := filepath.Join(item.DataDir, "raft.before-restore")
	if err := os.Rename(raftPath, retainedRaftPath); err != nil {
		t.Fatalf("retain original Raft state: %v", err)
	}
	if err := restoreCluster([]string{
		"--config", configPath, "--input", backupPath, "--confirm", item.ClusterID,
	}); err != nil {
		t.Fatalf("stage restore: %v", err)
	}

	process = startChaosNode(t, root, item)
	waitForAllReady(t, client, configs, 25*time.Second)
	if matches, err := filepath.Glob(filepath.Join(item.DataDir, "restore.applied.*.json")); err != nil || len(matches) != 1 {
		t.Fatalf("applied restore marker: matches=%v err=%v", matches, err)
	}
	if _, err := os.Stat(filepath.Join(retainedRaftPath, "raft.db")); err != nil {
		t.Fatalf("original Raft state was not retained: %v", err)
	}
	secondBackup := filepath.Join(root, "cluster-backup-restored.json")
	if err := backupCluster([]string{
		"--config", configPath, "--target", item.NodeID, "--output", secondBackup,
	}); err != nil {
		t.Fatalf("backup restored process: %v", err)
	}
}
