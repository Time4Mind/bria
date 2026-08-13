package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

type nodeContractState struct {
	Options    joinOptions `json:"options"`
	RequestID  string      `json:"request_id"`
	PrivateKey string      `json:"private_key"`
}

func createNodeContract(arguments []string) error {
	defaults, err := config.Default()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("cluster contract", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options, statePath := joinOptions{}, ""
	flags.StringVar(&options.NodeID, "node-id", "", "stable node identifier")
	flags.StringVar(&options.NodeName, "node-name", "", "display name")
	flags.StringVar(&options.DataDir, "data-dir", defaults.DataDir, "node data directory")
	flags.StringVar(&options.ConfigPath, "config", "", "config output path")
	flags.StringVar(&options.RaftBind, "raft-bind", defaults.RaftBind, "raft listen address")
	flags.StringVar(&options.RaftAdvertise, "raft-advertise", "", "routable raft address")
	flags.StringVar(&options.ControlBind, "control-bind", "", "control listen address")
	flags.StringVar(&options.ControlAdvertise, "control-advertise", "", "routable control address")
	flags.StringVar(&options.EnrollmentDialAddress, "enrollment-dial-address", "", "node-local enrollment tunnel endpoint")
	flags.StringVar(&statePath, "state", "", "private contract state path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || options.NodeName == "" || options.RaftAdvertise == "" || statePath == "" {
		return errors.New("node-name, raft-advertise, and state are required")
	}
	if options.NodeID == "" {
		id, err := newOperationID()
		if err != nil {
			return err
		}
		options.NodeID = "node-" + id[:12]
	}
	return writeNodeContract(options, statePath)
}

func writeNodeContract(options joinOptions, statePath string) error {
	dataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return err
	}
	options.DataDir = dataDir
	if options.ConfigPath == "" {
		options.ConfigPath = filepath.Join(dataDir, "config.json")
	}
	options.ConfigPath, err = filepath.Abs(options.ConfigPath)
	if err != nil {
		return err
	}
	network, err := joinNetwork(options)
	if err != nil {
		return err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	requestID, err := newOperationID()
	if err != nil {
		return err
	}
	contract, err := security.SignNodeContract(security.NodeContract{
		RequestID: requestID, NodeID: domain.NodeID(options.NodeID), Name: options.NodeName,
		Network: network, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}, privateKey)
	if err != nil {
		return err
	}
	state := nodeContractState{
		Options: options, RequestID: requestID,
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	statePath, err = filepath.Abs(statePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return err
	}
	if err := writeExclusive(statePath, append(encoded, '\n')); err != nil {
		return err
	}
	fmt.Println(contract)
	return nil
}
