package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/enrollment"
	"github.com/Time4Mind/bria/internal/security"
)

type joinOptions struct {
	Invitation       string
	NodeID           string
	NodeName         string
	DataDir          string
	ConfigPath       string
	RaftBind         string
	RaftAdvertise    string
	ControlBind      string
	ControlAdvertise string
	Wait             time.Duration
}

func joinCluster(arguments []string) error {
	defaults, err := config.Default()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("cluster join", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := joinOptions{}
	flags.StringVar(&options.Invitation, "invite", "", "one-time cluster invitation")
	flags.StringVar(&options.NodeID, "node-id", "", "stable node identifier")
	flags.StringVar(&options.NodeName, "node-name", "", "display name")
	flags.StringVar(&options.DataDir, "data-dir", defaults.DataDir, "node data directory")
	flags.StringVar(&options.ConfigPath, "config", "", "config output path")
	flags.StringVar(&options.RaftBind, "raft-bind", defaults.RaftBind, "raft listen address")
	flags.StringVar(&options.RaftAdvertise, "raft-advertise", "", "routable raft address")
	flags.StringVar(&options.ControlBind, "control-bind", "", "control listen address")
	flags.StringVar(&options.ControlAdvertise, "control-advertise", "", "routable control address")
	flags.DurationVar(&options.Wait, "wait", 24*time.Hour, "approval wait limit")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || options.Invitation == "" || options.NodeName == "" ||
		options.RaftAdvertise == "" || options.Wait <= 0 || options.Wait > 24*time.Hour {
		return errors.New("invite, node-name, raft-advertise, and a wait up to 24h are required")
	}
	if options.NodeID == "" {
		id, err := newOperationID()
		if err != nil {
			return err
		}
		options.NodeID = "node-" + id[:12]
	}
	return performClusterJoin(options)
}

func performClusterJoin(options joinOptions) error {
	invitation, err := security.DecodeClusterInvitation(options.Invitation, time.Now())
	if err != nil {
		return err
	}
	absoluteDataDir, err := filepath.Abs(options.DataDir)
	if err != nil {
		return err
	}
	options.DataDir = absoluteDataDir
	if options.ConfigPath == "" {
		options.ConfigPath = filepath.Join(absoluteDataDir, "config.json")
	}
	options.ConfigPath, err = filepath.Abs(options.ConfigPath)
	if err != nil {
		return err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	requestID, err := newOperationID()
	if err != nil {
		return err
	}
	network, err := joinNetwork(options)
	if err != nil {
		return err
	}
	contract, err := security.SignNodeContract(security.NodeContract{
		RequestID: requestID, NodeID: domain.NodeID(options.NodeID), Name: options.NodeName,
		Network: network, OS: runtime.GOOS, Arch: runtime.GOARCH,
		ExpiresAt: invitation.ExpiresAt,
	}, privateKey)
	if err != nil {
		return err
	}
	client, err := enrollment.NewClient(invitation, 10*time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Wait)
	defer cancel()
	registered, err := client.Register(ctx, invitation, contract)
	if err != nil {
		status, statusErr := client.Status(ctx, requestID, privateKey)
		if statusErr != nil || (status.Status != domain.EnrollmentPending &&
			status.Status != domain.EnrollmentApproved) {
			return err
		}
		registered.RequestID = requestID
	}
	bundle, err := waitForEnrollment(ctx, client, registered.RequestID, privateKey)
	if err != nil {
		return err
	}
	return installJoinedNode(options, invitation, network, privateKey, publicKey, bundle)
}

func joinNetwork(options joinOptions) (domain.NodeNetwork, error) {
	temporary := config.Config{
		RaftBind: options.RaftBind, RaftAdvertise: options.RaftAdvertise,
		ControlBind: options.ControlBind, ControlAdvertise: options.ControlAdvertise,
	}
	control, err := temporary.ControlAdvertiseAddress()
	if err != nil {
		return domain.NodeNetwork{}, err
	}
	return domain.NodeNetwork{RaftAddress: options.RaftAdvertise, ControlAddress: control}, nil
}

func waitForEnrollment(
	ctx context.Context,
	client *enrollment.Client,
	requestID string,
	privateKey ed25519.PrivateKey,
) (enrollment.ApprovedBundle, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		response, err := client.Status(ctx, requestID, privateKey)
		if err == nil {
			switch response.Status {
			case domain.EnrollmentApproved:
				if response.Bundle == nil {
					return enrollment.ApprovedBundle{}, errors.New("approved enrollment has no bundle")
				}
				return *response.Bundle, nil
			case domain.EnrollmentRejected:
				return enrollment.ApprovedBundle{}, errors.New("node enrollment was rejected")
			}
		}
		select {
		case <-ctx.Done():
			return enrollment.ApprovedBundle{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func encodeNodePrivateKey(privateKey ed25519.PrivateKey) ([]byte, error) {
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), nil
}
