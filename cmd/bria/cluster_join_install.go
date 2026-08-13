package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/enrollment"
	"github.com/Time4Mind/bria/internal/security"
)

func installJoinedNode(
	options joinOptions,
	invitation security.ClusterInvitation,
	network domain.NodeNetwork,
	privateKey ed25519.PrivateKey,
	_ ed25519.PublicKey,
	bundle enrollment.ApprovedBundle,
) error {
	if bundle.ClusterID != invitation.ClusterID || bundle.IssuerNodeID != invitation.IssuerNodeID {
		return errors.New("approved enrollment bundle identifies another cluster")
	}
	privateKeyPEM, err := encodeNodePrivateKey(privateKey)
	if err != nil {
		return err
	}
	callbackKey, err := base64.RawURLEncoding.DecodeString(bundle.CallbackKey)
	if err != nil {
		return errors.New("approved enrollment callback key is invalid")
	}
	for _, directory := range []string{
		options.DataDir, filepath.Join(options.DataDir, "pki"), filepath.Join(options.DataDir, "secrets"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return err
		}
	}
	configured, err := joinedNodeConfig(options, network, bundle)
	if err != nil {
		return err
	}
	files := []struct {
		path string
		data []byte
	}{
		{configured.CACertificate, []byte(bundle.CACertificate)},
		{configured.NodeCertificate, []byte(bundle.Certificate)},
		{configured.NodePrivateKey, privateKeyPEM},
		{configured.CallbackKeyFile, callbackKey},
	}
	for _, path := range append([]string{options.ConfigPath}, joinFilePaths(files)...) {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing file: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, file := range files {
		if err := writeExclusive(file.path, file.data); err != nil {
			return err
		}
	}
	if err := writeConfigNew(options.ConfigPath, configured); err != nil {
		return err
	}
	fmt.Printf("joined Bria cluster %s; config written to %s\n", bundle.ClusterID, options.ConfigPath)
	return nil
}

func joinFilePaths(files []struct {
	path string
	data []byte
}) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	return paths
}

func joinedNodeConfig(
	options joinOptions,
	network domain.NodeNetwork,
	bundle enrollment.ApprovedBundle,
) (config.Config, error) {
	defaults, err := config.Default()
	if err != nil {
		return config.Config{}, err
	}
	name := options.NodeName
	peers := make([]config.RaftPeer, 0, len(bundle.Peers))
	for _, peer := range bundle.Peers {
		peers = append(peers, config.RaftPeer{
			NodeID: peer.NodeID, NodeName: peer.Name, Address: peer.RaftAddress,
			ControlAddress: peer.ControlAddress,
		})
		if peer.NodeID == options.NodeID {
			name = peer.Name
		}
	}
	configured := defaults
	configured.ClusterID, configured.NodeID, configured.NodeName = bundle.ClusterID, options.NodeID, name
	configured.DataDir = options.DataDir
	configured.RaftBind, configured.RaftAdvertise = options.RaftBind, network.RaftAddress
	configured.ControlBind, configured.ControlAdvertise = options.ControlBind, network.ControlAddress
	configured.EnrollmentAdvertise = bundle.EnrollmentAddress
	configured.EnrollmentIssuerID = bundle.IssuerNodeID
	configured.RaftPeers, configured.Bootstrap = peers, false
	configured.CACertificate = filepath.Join(options.DataDir, "pki", "ca.crt")
	configured.NodeCertificate = filepath.Join(options.DataDir, "pki", "node.crt")
	configured.NodePrivateKey = filepath.Join(options.DataDir, "pki", "node.key")
	configured.TelegramTokenFile = filepath.Join(options.DataDir, "secrets", "telegram.token")
	configured.CallbackKeyFile = filepath.Join(options.DataDir, "secrets", "callback.key")
	configured.WhisperModelPath = filepath.Join(options.DataDir, "models", filepath.Base(defaults.WhisperModelPath))
	if err := configured.Validate(); err != nil {
		return config.Config{}, err
	}
	return configured, nil
}
