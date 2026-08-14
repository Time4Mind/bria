package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

const (
	recoveredCAValidity   = 10 * 365 * 24 * time.Hour
	recoveredNodeValidity = 365 * 24 * time.Hour
)

// recoverClusterCA is deliberately limited to a recently backed-up one-node
// cluster. It is an emergency path for a lost bootstrap CA key, not a general
// CA rotation mechanism. Existing Raft data stays untouched.
func recoverClusterCA(arguments []string) error {
	flags := flag.NewFlagSet("cluster recover-ca", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "node config path")
	backupPath := flags.String("backup", "", "recent signed logical backup")
	confirmation := flags.String("confirm", "", "repeat cluster ID")
	maxBackupAge := flags.Duration("max-backup-age", 15*time.Minute, "maximum accepted backup age")
	enrollmentBind := flags.String("enrollment-bind", "", "optional enrollment listen address")
	enrollmentAdvertise := flags.String("enrollment-advertise", "", "optional advertised enrollment address")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *backupPath == "" || *confirmation == "" ||
		*maxBackupAge < time.Minute || *maxBackupAge > 24*time.Hour {
		return errors.New("usage: bria cluster recover-ca --config PATH --backup FILE --confirm CLUSTER_ID")
	}
	source, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *confirmation != source.ClusterID {
		return errors.New("confirmation must exactly match cluster ID")
	}
	if source.Bootstrap || source.CAPrivateKey != "" {
		return errors.New("CA recovery is only allowed when the configured bootstrap CA key is absent")
	}
	listener, err := net.Listen("tcp", source.RaftBind)
	if err != nil {
		return errors.New("stop the Bria node before recovering the cluster CA")
	}
	if err := listener.Close(); err != nil {
		return fmt.Errorf("release offline check listener: %w", err)
	}

	backup, state, _, err := loadRestoreCandidate(*backupPath, source)
	if err != nil {
		return fmt.Errorf("verify recovery backup: %w", err)
	}
	now := time.Now().UTC()
	if backup.CreatedAt.After(now.Add(time.Minute)) || now.Sub(backup.CreatedAt) > *maxBackupAge {
		return errors.New("recovery backup is not recent; create a new logical backup")
	}
	if err := validateSingleNodeRecoveryState(state, source.NodeID); err != nil {
		return err
	}

	oldCertificatePEM, err := os.ReadFile(source.NodeCertificate)
	if err != nil {
		return fmt.Errorf("read active node certificate: %w", err)
	}
	oldCertificate, err := security.ParseCertificate(oldCertificatePEM)
	if err != nil {
		return fmt.Errorf("parse active node certificate: %w", err)
	}
	oldFingerprint, err := security.NodeCertificateFingerprint(oldCertificate)
	if err != nil {
		return err
	}
	ca, caPEM, caKeyPEM, err := security.GenerateCA(
		source.ClusterID, now, recoveredCAValidity,
	)
	if err != nil {
		return err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate replacement node key: %w", err)
	}
	certificatePEM, err := security.IssueRotatedNodeCertificateForPublicKey(
		ca, source.ClusterID, source.NodeID, publicKey, oldFingerprint,
		now, recoveredNodeValidity,
	)
	if err != nil {
		return err
	}
	privateKeyPEM, err := security.MarshalEd25519PrivateKey(privateKey)
	if err != nil {
		return err
	}

	recoveryRoot := filepath.Join(source.DataDir, "pki", "ca-recovery")
	if err := os.MkdirAll(recoveryRoot, 0o700); err != nil {
		return fmt.Errorf("create CA recovery directory: %w", err)
	}
	if err := os.Chmod(recoveryRoot, 0o700); err != nil {
		return fmt.Errorf("secure CA recovery directory: %w", err)
	}
	recoveryDirectory, err := os.MkdirTemp(recoveryRoot, now.Format("20060102T150405Z")+"-")
	if err != nil {
		return fmt.Errorf("create CA recovery generation: %w", err)
	}
	if err := os.Chmod(recoveryDirectory, 0o700); err != nil {
		return fmt.Errorf("secure CA recovery generation: %w", err)
	}
	caPath := filepath.Join(recoveryDirectory, "ca.crt")
	caKeyPath := filepath.Join(recoveryDirectory, "ca.key")
	nodeCertificatePath := filepath.Join(recoveryDirectory, "node.crt")
	nodeKeyPath := filepath.Join(recoveryDirectory, "node.key")
	for _, file := range []struct {
		path string
		data []byte
	}{
		{caPath, caPEM},
		{caKeyPath, caKeyPEM},
		{nodeCertificatePath, certificatePEM},
		{nodeKeyPath, privateKeyPEM},
	} {
		if err := writeExclusive(file.path, file.data); err != nil {
			return err
		}
	}

	recovered := source
	recovered.Bootstrap = true
	recovered.CACertificate = caPath
	recovered.CAPrivateKey = caKeyPath
	recovered.NodeCertificate = nodeCertificatePath
	recovered.NodePrivateKey = nodeKeyPath
	recovered.EnrollmentIssuerID = source.NodeID
	recovered.EnrollmentBind = *enrollmentBind
	recovered.EnrollmentAdvertise = *enrollmentAdvertise
	recovered.RaftPeers = []config.RaftPeer{recoveredSelfPeer(source)}
	if err := recovered.Validate(); err != nil {
		return fmt.Errorf("validate recovered config: %w", err)
	}
	if err := verifyRecoveredIdentity(recovered, oldFingerprint); err != nil {
		return err
	}
	if err := writeConfigAtomic(*configPath, recovered); err != nil {
		return err
	}
	fmt.Printf(
		"recovered bootstrap CA for %s; old Raft state was preserved; restart node %s\n",
		source.ClusterID, source.NodeID,
	)
	return nil
}

func validateSingleNodeRecoveryState(state *domain.State, nodeID string) error {
	if state == nil {
		return errors.New("recovery backup contains no cluster state")
	}
	active := make([]domain.NodeID, 0, len(state.Nodes))
	for candidateID, node := range state.Nodes {
		if node.Enabled() {
			active = append(active, candidateID)
		}
	}
	if len(active) != 1 || active[0] != domain.NodeID(nodeID) {
		return errors.New("CA recovery requires a backup with exactly the local node enabled")
	}
	return nil
}

func recoveredSelfPeer(source config.Config) config.RaftPeer {
	peer := config.RaftPeer{
		NodeID: source.NodeID, NodeName: source.NodeName, Address: source.RaftAdvertise,
	}
	for _, candidate := range source.RaftPeers {
		if candidate.NodeID == source.NodeID {
			peer = candidate
			peer.NodeName = source.NodeName
			peer.Address = source.RaftAdvertise
			break
		}
	}
	return peer
}

func verifyRecoveredIdentity(recovered config.Config, oldFingerprint string) error {
	if _, _, err := loadNodeTLS(recovered); err != nil {
		return fmt.Errorf("verify recovered TLS identity: %w", err)
	}
	pair, err := tls.LoadX509KeyPair(recovered.NodeCertificate, recovered.NodePrivateKey)
	if err != nil {
		return fmt.Errorf("load recovered node key pair: %w", err)
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse recovered node certificate: %w", err)
	}
	previous, present, err := security.PreviousNodeCertificateFingerprint(certificate)
	if err != nil || !present || previous != oldFingerprint {
		return errors.New("recovered node certificate is not linked to the previous identity")
	}
	return nil
}
