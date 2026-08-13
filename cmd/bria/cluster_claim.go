package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Time4Mind/bria/internal/enrollment"
	"github.com/Time4Mind/bria/internal/security"
)

func claimNodeContract(arguments []string) error {
	flags := flag.NewFlagSet("cluster claim", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	claimValue := flags.String("claim", "", "approved enrollment claim")
	statePath := flags.String("state", "", "private contract state path")
	wait := flags.Duration("wait", 24*time.Hour, "claim wait limit")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *claimValue == "" || *statePath == "" ||
		*wait <= 0 || *wait > 24*time.Hour {
		return errors.New("claim, state, and a wait up to 24h are required")
	}
	claim, err := security.DecodeEnrollmentClaim(*claimValue, time.Now())
	if err != nil {
		return err
	}
	state, privateKey, absoluteState, err := loadNodeContractState(*statePath)
	if err != nil {
		return err
	}
	if state.RequestID != claim.RequestID {
		return errors.New("claim does not match private contract state")
	}
	client, err := enrollment.NewClaimClient(claim, 10*time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wait)
	defer cancel()
	bundle, err := waitForEnrollment(ctx, client, claim.RequestID, privateKey)
	if err != nil {
		return err
	}
	trust := security.ClusterInvitation{
		ClusterID: claim.ClusterID, IssuerNodeID: claim.IssuerNodeID,
	}
	network, err := joinNetwork(state.Options)
	if err != nil {
		return err
	}
	if err := installJoinedNode(state.Options, trust, network, privateKey, nil, bundle); err != nil {
		return err
	}
	return os.Remove(absoluteState)
}

func loadNodeContractState(path string) (nodeContractState, ed25519.PrivateKey, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nodeContractState{}, nil, "", err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return nodeContractState{}, nil, "", errors.New("private contract state permissions are unsafe")
	}
	data, err := os.ReadFile(absolute)
	if err != nil || len(data) > 32<<10 {
		return nodeContractState{}, nil, "", errors.New("read private contract state")
	}
	var state nodeContractState
	if json.Unmarshal(data, &state) != nil || state.RequestID == "" {
		return nodeContractState{}, nil, "", errors.New("invalid private contract state")
	}
	key, err := base64.RawURLEncoding.DecodeString(state.PrivateKey)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		return nodeContractState{}, nil, "", errors.New("invalid private contract key")
	}
	return state, ed25519.PrivateKey(key), absolute, nil
}
