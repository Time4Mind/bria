package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
)

func applyPendingClusterRestore(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
) error {
	pendingPath := filepath.Join(nodeConfig.DataDir, pendingRestoreName)
	encoded, err := os.ReadFile(pendingPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read pending cluster restore: %w", err)
	}
	backup, _, _, err := loadRestoreCandidate(pendingPath, nodeConfig)
	if err != nil {
		return fmt.Errorf("validate pending cluster restore: %w", err)
	}
	digest := sha256.Sum256(encoded)
	operationID := "restore-cluster-" + hex.EncodeToString(digest[:16])
	command, err := clusterstate.NewCommand(
		operationID, clusterstate.CommandRestoreCluster, time.Now(),
		clusterstate.RestoreCluster{
			BackupSHA256: backup.SnapshotSHA256,
			Snapshot:     backup.Snapshot,
		},
	)
	if err != nil {
		return err
	}
	result, err := node.Apply(ctx, command)
	if err != nil {
		return fmt.Errorf("commit cluster restore: %w", err)
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("commit cluster restore: %w", err)
	}
	appliedPath := filepath.Join(
		nodeConfig.DataDir, "restore.applied."+hex.EncodeToString(digest[:8])+".json",
	)
	if err := os.Rename(pendingPath, appliedPath); err != nil {
		return fmt.Errorf("mark cluster restore applied: %w", err)
	}
	directory, err := os.Open(nodeConfig.DataDir)
	if err != nil {
		return fmt.Errorf("open restore directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync restore directory: %w", err)
	}
	return nil
}
