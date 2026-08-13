package main

import (
	"crypto/rand"
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

func initCluster(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "init" {
		return errors.New("usage: bria cluster init --cluster-id ID --node-id ID --node-name NAME")
	}
	defaults, err := config.Default()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("cluster init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	clusterID := flags.String("cluster-id", "", "stable cluster identifier")
	nodeID := flags.String("node-id", "", "stable node identifier")
	nodeName := flags.String("node-name", "", "display name")
	ownerUserID := flags.Int64("owner-user-id", 0, "initial Telegram owner user ID")
	dataDir := flags.String("data-dir", defaults.DataDir, "node data directory")
	configPath := flags.String("config", "", "config file path")
	bind := flags.String("raft-bind", defaults.RaftBind, "raft listen address")
	advertise := flags.String("raft-advertise", defaults.RaftAdvertise, "raft advertised address")
	updateManifest := flags.String("update-manifest-url", defaults.UpdateManifestURL, "signed release manifest URL")
	updatePublicKey := flags.String("update-public-key", defaults.UpdatePublicKey, "base64 Ed25519 release public key")
	runnerMode := flags.String("runner-mode", config.RunnerModeTrusted, "backend runner: trusted, docker, native-user, or wsl")
	runnerSocket := flags.String("runner-socket", "", "isolated runner Unix socket")
	runnerHome := flags.String("runner-home", "", "runner home visible read-only to control")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *clusterID == "" || *nodeID == "" || *nodeName == "" {
		return errors.New("cluster-id, node-id, and node-name are required")
	}
	absoluteDataDir, err := filepath.Abs(*dataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	if *configPath == "" {
		*configPath = filepath.Join(absoluteDataDir, "config.json")
	}
	absoluteConfigPath, err := filepath.Abs(*configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}
	pkiDir := filepath.Join(absoluteDataDir, "pki")
	secretsDir := filepath.Join(absoluteDataDir, "secrets")
	for _, directory := range []string{absoluteDataDir, pkiDir, secretsDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", directory, err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure %s: %w", directory, err)
		}
	}
	nodeConfig := config.Config{
		ClusterID: *clusterID, NodeID: *nodeID, NodeName: *nodeName,
		BootstrapOwnerID: *ownerUserID,
		DataDir:          absoluteDataDir, RaftBind: *bind, RaftAdvertise: *advertise,
		Bootstrap:          true,
		CACertificate:      filepath.Join(pkiDir, "ca.crt"),
		CAPrivateKey:       filepath.Join(pkiDir, "ca.key"),
		NodeCertificate:    filepath.Join(pkiDir, "node.crt"),
		NodePrivateKey:     filepath.Join(pkiDir, "node.key"),
		TelegramTokenFile:  filepath.Join(secretsDir, "telegram.token"),
		CallbackKeyFile:    filepath.Join(secretsDir, "callback.key"),
		TmuxSession:        "bria",
		ClaudeCommand:      "claude",
		ClaudeNamingModel:  "haiku",
		CodexCommand:       "codex",
		CodexNamingModel:   "gpt-5.6-luna",
		SpeechEngine:       config.SpeechEngineWhisper,
		FFmpegCommand:      "ffmpeg",
		WhisperCommand:     "whisper-cli",
		WhisperModelPath:   filepath.Join(absoluteDataDir, "models", "ggml-medium-q8_0.bin"),
		WhisperLanguage:    "auto",
		WhisperThreads:     4,
		AppleSpeechCommand: "bria-apple-speech",
		UpdateManifestURL:  *updateManifest,
		UpdatePublicKey:    *updatePublicKey,
		Runner: config.RunnerConfig{
			Mode: *runnerMode, Socket: *runnerSocket, Home: *runnerHome,
		},
	}
	if err := nodeConfig.Validate(); err != nil {
		return err
	}
	paths := []string{
		absoluteConfigPath, nodeConfig.CACertificate, nodeConfig.CAPrivateKey,
		nodeConfig.NodeCertificate, nodeConfig.NodePrivateKey, nodeConfig.CallbackKeyFile,
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing file: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect target %s: %w", path, err)
		}
	}
	now := time.Now().UTC()
	ca, caPEM, caKeyPEM, err := security.GenerateCA(*clusterID, now, 10*365*24*time.Hour)
	if err != nil {
		return err
	}
	credentials, err := security.IssueNodeCertificate(ca, *clusterID, *nodeID, now, 365*24*time.Hour)
	if err != nil {
		return err
	}
	encodedConfig, err := json.MarshalIndent(nodeConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	callbackKey := make([]byte, callbacktoken.KeyBytes)
	if _, err := rand.Read(callbackKey); err != nil {
		return fmt.Errorf("generate callback key: %w", err)
	}
	files := []struct {
		path string
		data []byte
	}{
		{nodeConfig.CACertificate, caPEM},
		{nodeConfig.CAPrivateKey, caKeyPEM},
		{nodeConfig.NodeCertificate, credentials.CertificatePEM},
		{nodeConfig.NodePrivateKey, credentials.PrivateKeyPEM},
		{nodeConfig.CallbackKeyFile, callbackKey},
		{absoluteConfigPath, append(encodedConfig, '\n')},
	}
	for _, file := range files {
		if err := writeExclusive(file.path, file.data); err != nil {
			return err
		}
	}
	fmt.Printf("initialized Bria cluster %s at %s\n", *clusterID, absoluteConfigPath)
	return nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
