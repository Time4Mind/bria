package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/nodecontrol"
)

func backupCluster(arguments []string) error {
	flags := flag.NewFlagSet("cluster backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to node config JSON")
	outputPath := flags.String("output", "", "new backup file path")
	target := flags.String("target", "", "current leader node ID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *outputPath == "" {
		return errors.New("usage: bria cluster backup --config PATH --output FILE [--target NODE]")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *target == "" {
		*target = nodeConfig.NodeID
	}
	absoluteOutput, err := filepath.Abs(*outputPath)
	if err != nil {
		return fmt.Errorf("resolve backup output: %w", err)
	}
	if _, err := os.Stat(absoluteOutput); err == nil {
		return fmt.Errorf("refusing to overwrite existing file: %s", absoluteOutput)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup output: %w", err)
	}
	resolver, err := controlResolver(nodeConfig, nil)
	if err != nil {
		return err
	}
	certificate, roots, err := loadNodeTLS(nodeConfig)
	if err != nil {
		return err
	}
	client, err := nodecontrol.NewClient(nodecontrol.ClientConfig{
		Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
		Resolver: resolver, Timeout: 30 * time.Second,
	})
	if err != nil {
		return err
	}
	defer func() {
		if client != nil {
			client.CloseIdleConnections()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	currentTarget := *target
	var backup clusterbackup.Envelope
	for attempt := 0; attempt < 8; attempt++ {
		backup, err = client.Backup(ctx, currentTarget)
		var leaderErr *nodecontrol.BackupLeaderError
		if errors.As(err, &leaderErr) && leaderErr.Hint.NodeID != currentTarget {
			client.CloseIdleConnections()
			currentTarget = leaderErr.Hint.NodeID
			client, err = nodecontrol.NewClient(nodecontrol.ClientConfig{
				Certificate: certificate, Roots: roots, ClusterID: nodeConfig.ClusterID,
				Resolver: backupHintResolver{hint: leaderErr.Hint}, Timeout: 30 * time.Second,
			})
			if err == nil {
				continue
			}
		}
		var statusErr *nodecontrol.BackupStatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == 409 && attempt < 7 {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}
		break
	}
	if err != nil {
		return err
	}
	if backup.ClusterID != nodeConfig.ClusterID {
		return errors.New("leader returned a backup for another cluster")
	}
	if _, err := verifyBackupEnvelope(backup, roots); err != nil {
		return fmt.Errorf("verify cluster backup: %w", err)
	}
	encoded, err := backup.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absoluteOutput), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if err := writeExclusive(absoluteOutput, encoded); err != nil {
		return err
	}
	fmt.Printf("wrote logical cluster backup to %s\n", absoluteOutput)
	return nil
}

type backupHintResolver struct {
	hint nodecontrol.BackupLeaderHint
}

func (r backupHintResolver) ControlAddress(nodeID string) (string, bool) {
	return r.hint.ControlAddress, nodeID == r.hint.NodeID
}

func (r backupHintResolver) NodeFingerprint(nodeID string) (string, bool) {
	return r.hint.Fingerprint, nodeID == r.hint.NodeID && r.hint.Fingerprint != ""
}
