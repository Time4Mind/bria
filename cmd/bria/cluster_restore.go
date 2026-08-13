package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/clusterbackup"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

const pendingRestoreName = "restore.pending.json"

func restoreCluster(arguments []string) error {
	flags := flag.NewFlagSet("cluster restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "bootstrap node config path")
	inputPath := flags.String("input", "", "logical backup file")
	dryRun := flags.Bool("dry-run", false, "validate without staging restore")
	confirmation := flags.String("confirm", "", "repeat cluster ID to stage restore")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *inputPath == "" {
		return errors.New("usage: bria cluster restore --config PATH --input FILE [--dry-run|--confirm CLUSTER_ID]")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !nodeConfig.Bootstrap {
		return errors.New("restore can only be staged on a bootstrap node")
	}
	backup, state, encoded, err := loadRestoreCandidate(*inputPath, nodeConfig)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Printf(
			"backup valid: cluster=%s source=%s nodes=%d sessions=%d created=%s\n",
			backup.ClusterID, backup.SourceNodeID, len(state.Nodes), len(state.Sessions),
			backup.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		)
		return nil
	}
	if err := ensureCleanRaftDirectory(filepath.Join(nodeConfig.DataDir, "raft")); err != nil {
		return err
	}
	if *confirmation != nodeConfig.ClusterID {
		return errors.New("confirmation must exactly match cluster ID")
	}
	pendingPath := filepath.Join(nodeConfig.DataDir, pendingRestoreName)
	if err := writeExclusive(pendingPath, encoded); err != nil {
		return fmt.Errorf("stage cluster restore: %w", err)
	}
	fmt.Printf("restore staged at %s; start the bootstrap node to apply it once\n", pendingPath)
	return nil
}

func loadRestoreCandidate(
	path string,
	nodeConfig config.Config,
) (clusterbackup.Envelope, *domain.State, []byte, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, fmt.Errorf("read backup: %w", err)
	}
	backup, err := clusterbackup.Parse(encoded)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, err
	}
	if backup.ClusterID != nodeConfig.ClusterID {
		return clusterbackup.Envelope{}, nil, nil, errors.New("backup belongs to another cluster")
	}
	caPEM, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, fmt.Errorf("read cluster CA: %w", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, err
	}
	state, err := verifyBackupEnvelope(backup, roots)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, err
	}
	localTLS, _, err := loadNodeTLS(nodeConfig)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, err
	}
	if len(localTLS.Certificate) == 0 {
		return clusterbackup.Envelope{}, nil, nil, errors.New("local node certificate is empty")
	}
	certificate, err := x509.ParseCertificate(localTLS.Certificate[0])
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, fmt.Errorf("parse local node certificate: %w", err)
	}
	if err := security.VerifyNodeCertificate(
		certificate, roots, nodeConfig.ClusterID, nodeConfig.NodeID,
		time.Now(), x509.ExtKeyUsageServerAuth,
	); err != nil {
		return clusterbackup.Envelope{}, nil, nil, fmt.Errorf("verify local node certificate: %w", err)
	}
	fingerprint, err := security.NodeCertificateFingerprint(certificate)
	if err != nil {
		return clusterbackup.Envelope{}, nil, nil, err
	}
	local, ok := state.Nodes[domain.NodeID(nodeConfig.NodeID)]
	if !ok || !local.Enabled() || local.Fingerprint != fingerprint {
		return clusterbackup.Envelope{}, nil, nil, errors.New("backup does not contain the active local node identity")
	}
	return backup, state, encoded, nil
}

func verifyBackupEnvelope(
	backup clusterbackup.Envelope,
	roots *x509.CertPool,
) (*domain.State, error) {
	state, err := clusterstate.InspectSnapshot(backup.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("inspect backup snapshot: %w", err)
	}
	signerCertificate, err := security.ParseCertificate(backup.CertificatePEM)
	if err != nil {
		return nil, fmt.Errorf("parse backup signer: %w", err)
	}
	if err := security.VerifyNodeCertificate(
		signerCertificate, roots, backup.ClusterID, backup.SourceNodeID,
		backup.CreatedAt, x509.ExtKeyUsageServerAuth,
	); err != nil {
		return nil, fmt.Errorf("verify backup signer: %w", err)
	}
	publicKey, ok := signerCertificate.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("backup signer key is not Ed25519")
	}
	if err := backup.VerifySignature(publicKey); err != nil {
		return nil, err
	}
	signerFingerprint, err := security.NodeCertificateFingerprint(signerCertificate)
	if err != nil {
		return nil, err
	}
	source, ok := state.Nodes[domain.NodeID(backup.SourceNodeID)]
	if !ok || !source.Enabled() || source.Fingerprint != signerFingerprint {
		return nil, errors.New("backup signer is not active in the backed-up state")
	}
	return state, nil
}

func ensureCleanRaftDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Raft directory: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("restore requires a fresh Raft directory; existing cluster state was not changed")
	}
	return nil
}
