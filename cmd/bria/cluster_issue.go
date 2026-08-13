package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

func issueClusterNode(arguments []string) error {
	flags := flag.NewFlagSet("cluster issue-node", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	sourcePath := flags.String("config", "", "bootstrap node config path")
	nodeID := flags.String("node-id", "", "approved node identifier")
	outputDir := flags.String("output", "", "new bundle directory")
	dataDir := flags.String("data-dir", "/var/lib/bria", "target data directory")
	bind := flags.String("raft-bind", "", "target Raft bind address")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *sourcePath == "" || *nodeID == "" || *outputDir == "" {
		return errors.New("config, node-id, and output are required")
	}
	source, err := config.Load(*sourcePath)
	if err != nil {
		return err
	}
	if !source.Bootstrap || source.CAPrivateKey == "" {
		return errors.New("node certificates may only be issued from the bootstrap CA config")
	}
	peer, ok := findRaftPeer(source.RaftPeers, *nodeID)
	if !ok {
		return fmt.Errorf("node %q is not in the approved Raft peer set", *nodeID)
	}
	if *bind == "" {
		*bind = peer.Address
	}
	target, err := issuedNodeConfig(source, peer, *dataDir, *bind)
	if err != nil {
		return err
	}
	caCertificate, err := os.ReadFile(source.CACertificate)
	if err != nil {
		return fmt.Errorf("read CA certificate: %w", err)
	}
	caPrivateKey, err := os.ReadFile(source.CAPrivateKey)
	if err != nil {
		return fmt.Errorf("read CA private key: %w", err)
	}
	ca, err := security.ParseCA(caCertificate, caPrivateKey)
	if err != nil {
		return err
	}
	credentials, err := security.IssueNodeCertificate(
		ca, source.ClusterID, peer.NodeID, time.Now().UTC(), 365*24*time.Hour,
	)
	if err != nil {
		return err
	}
	callbackKey, err := os.ReadFile(source.CallbackKeyFile)
	if err != nil {
		return fmt.Errorf("read callback key: %w", err)
	}
	if len(callbackKey) != callbacktoken.KeyBytes {
		return fmt.Errorf("callback key must contain exactly %d bytes", callbacktoken.KeyBytes)
	}
	encodedConfig, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return fmt.Errorf("encode issued config: %w", err)
	}
	files := map[string][]byte{
		"config.json":  append(encodedConfig, '\n'),
		"ca.crt":       caCertificate,
		"node.crt":     credentials.CertificatePEM,
		"node.key":     credentials.PrivateKeyPEM,
		"callback.key": callbackKey,
	}
	if err := writeBundleExclusive(*outputDir, files); err != nil {
		return err
	}
	fmt.Printf("issued node bundle for %s at %s\n", peer.NodeID, *outputDir)
	return nil
}

func issuedNodeConfig(
	source config.Config,
	peer config.RaftPeer,
	dataDir string,
	bind string,
) (config.Config, error) {
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return config.Config{}, fmt.Errorf("resolve target data directory: %w", err)
	}
	enrollmentAddress, err := source.EnrollmentAdvertiseAddress()
	if err != nil {
		return config.Config{}, fmt.Errorf("resolve enrollment address: %w", err)
	}
	modelName := filepath.Base(source.WhisperModelPath)
	if modelName == "." || modelName == string(filepath.Separator) {
		modelName = "ggml-medium-q8_0.bin"
	}
	target := config.Config{
		ClusterID: source.ClusterID, NodeID: peer.NodeID, NodeName: peer.NodeName,
		DataDir: absoluteDataDir, RaftBind: bind, RaftAdvertise: peer.Address,
		RaftPeers: append([]config.RaftPeer(nil), source.RaftPeers...), Bootstrap: false,
		EnrollmentAdvertise: enrollmentAddress,
		EnrollmentIssuerID:  source.EffectiveEnrollmentIssuerID(),
		CACertificate:       filepath.Join(absoluteDataDir, "pki", "ca.crt"),
		NodeCertificate:     filepath.Join(absoluteDataDir, "pki", "node.crt"),
		NodePrivateKey:      filepath.Join(absoluteDataDir, "pki", "node.key"),
		TelegramTokenFile:   filepath.Join(absoluteDataDir, "secrets", "telegram.token"),
		CallbackKeyFile:     filepath.Join(absoluteDataDir, "secrets", "callback.key"),
		TmuxSession:         "bria", ClaudeCommand: source.ClaudeCommand,
		ClaudeNamingModel:  source.ClaudeNamingModel,
		ClaudeFlags:        append([]string(nil), source.ClaudeFlags...),
		CodexCommand:       source.CodexCommand,
		CodexNamingModel:   source.CodexNamingModel,
		CodexFlags:         append([]string(nil), source.CodexFlags...),
		SpeechEngine:       source.EffectiveSpeechEngine(),
		FFmpegCommand:      source.FFmpegCommand,
		WhisperCommand:     source.WhisperCommand,
		WhisperModelPath:   filepath.Join(absoluteDataDir, "models", modelName),
		WhisperLanguage:    source.WhisperLanguage,
		WhisperThreads:     source.WhisperThreads,
		AppleSpeechCommand: source.AppleSpeechCommand,
	}
	if err := target.Validate(); err != nil {
		return config.Config{}, err
	}
	return target, nil
}

func findRaftPeer(peers []config.RaftPeer, nodeID string) (config.RaftPeer, bool) {
	for _, peer := range peers {
		if peer.NodeID == nodeID {
			return peer, true
		}
	}
	return config.RaftPeer{}, false
}

func writeBundleExclusive(outputDir string, files map[string][]byte) error {
	absoluteOutput, err := filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve bundle path: %w", err)
	}
	if _, err := os.Stat(absoluteOutput); err == nil {
		return fmt.Errorf("refusing to overwrite existing path: %s", absoluteOutput)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect bundle path: %w", err)
	}
	parent := filepath.Dir(absoluteOutput)
	temporary, err := os.MkdirTemp(parent, ".bria-bundle-*")
	if err != nil {
		return fmt.Errorf("create bundle staging directory: %w", err)
	}
	defer os.RemoveAll(temporary)
	if err := os.Chmod(temporary, 0o700); err != nil {
		return fmt.Errorf("secure bundle directory: %w", err)
	}
	for name, data := range files {
		if err := writeExclusive(filepath.Join(temporary, name), data); err != nil {
			return err
		}
	}
	if err := os.Rename(temporary, absoluteOutput); err != nil {
		return fmt.Errorf("publish node bundle: %w", err)
	}
	return nil
}
