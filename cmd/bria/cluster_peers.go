package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Time4Mind/bria/internal/config"
)

type repeatedFlag []string

func (values *repeatedFlag) String() string { return strings.Join(*values, ",") }

func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func setClusterPeers(arguments []string) error {
	flags := flag.NewFlagSet("cluster set-peers", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "bootstrap node config path")
	selfBind := flags.String("self-bind", "", "local Raft bind address")
	var peerValues repeatedFlag
	flags.Var(&peerValues, "peer", "node-id,node-name,address (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *selfBind == "" || len(peerValues) == 0 {
		return errors.New("config, self-bind, and at least one peer are required")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	peers, err := parseRaftPeers(peerValues)
	if err != nil {
		return err
	}
	nodeConfig.RaftBind = *selfBind
	for _, peer := range peers {
		if peer.NodeID == nodeConfig.NodeID {
			nodeConfig.RaftAdvertise = peer.Address
		}
	}
	nodeConfig.RaftPeers = peers
	if err := nodeConfig.Validate(); err != nil {
		return err
	}
	if err := writeConfigAtomic(*configPath, nodeConfig); err != nil {
		return err
	}
	fmt.Printf("configured %d approved Raft peers in %s\n", len(peers), *configPath)
	return nil
}

func parseRaftPeers(values []string) ([]config.RaftPeer, error) {
	peers := make([]config.RaftPeer, 0, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, ",", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid peer %q: want node-id,node-name,address", value)
		}
		peers = append(peers, config.RaftPeer{
			NodeID: strings.TrimSpace(parts[0]), NodeName: strings.TrimSpace(parts[1]),
			Address: strings.TrimSpace(parts[2]),
		})
	}
	return peers, nil
}

func writeConfigAtomic(path string, nodeConfig config.Config) (returnErr error) {
	encoded, err := json.MarshalIndent(nodeConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect config: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".bria-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if returnErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if err := preserveFileOwner(temporary, info); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("preserve config ownership: %w", err)
	}
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open config directory: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}
