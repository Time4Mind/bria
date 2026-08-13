package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/enrollment"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/security"
)

type enrollmentRuntime struct {
	server   *enrollment.Server
	listener net.Listener
}

type enrollmentRoute struct {
	node   *consensus.Node
	local  *nodecontrol.ConsensusEnrollmentCommitter
	remote interface {
		ReportEnrollment(context.Context, string, nodecontrol.EnrollmentReport) error
	}
}

func (r enrollmentRoute) SubmitEnrollment(
	ctx context.Context,
	request domain.EnrollmentRequest,
	expectedHash string,
) error {
	report := nodecontrol.EnrollmentReport{
		ReportID: request.ID, Request: request, ExpectedHash: expectedHash,
	}
	if r.node.IsLeader() {
		return r.local.CommitEnrollment(ctx, report)
	}
	leaderID := r.node.LeaderID()
	if leaderID == "" {
		return errors.New("Raft leader is not known")
	}
	return r.remote.ReportEnrollment(ctx, leaderID, report)
}

func startEnrollmentRuntime(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
	certificate tls.Certificate,
	client *nodecontrol.Client,
	committer *nodecontrol.ConsensusEnrollmentCommitter,
) (*enrollmentRuntime, error) {
	if !nodeConfig.Bootstrap {
		return nil, nil
	}
	caPEM, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return nil, err
	}
	caKey, err := os.ReadFile(nodeConfig.CAPrivateKey)
	if err != nil {
		return nil, err
	}
	ca, err := security.ParseCA(caPEM, caKey)
	if err != nil {
		return nil, err
	}
	callbackKey, err := os.ReadFile(nodeConfig.CallbackKeyFile)
	if err != nil || len(callbackKey) != callbacktoken.KeyBytes {
		return nil, errors.New("read enrollment callback key")
	}
	address, err := nodeConfig.EnrollmentAdvertiseAddress()
	if err != nil {
		return nil, err
	}
	server, err := enrollment.NewServer(enrollment.ServerConfig{
		ClusterID: nodeConfig.ClusterID, IssuerNodeID: nodeConfig.NodeID,
		EnrollmentAddress: address, Certificate: certificate, CA: ca,
		CAPEM: caPEM, CallbackKey: callbackKey, State: node.State(),
		UpdateManifestURL: nodeConfig.UpdateManifestURL,
		UpdatePublicKey:   nodeConfig.UpdatePublicKey,
		Submit:            enrollmentRoute{node: node, local: committer, remote: client},
	})
	if err != nil {
		return nil, err
	}
	bind, err := nodeConfig.EnrollmentBindAddress()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, fmt.Errorf("listen for enrollment: %w", err)
	}
	runtime := &enrollmentRuntime{server: server, listener: listener}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "bria enrollment: %v\n", serveErr)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	return runtime, nil
}

func closeEnrollmentRuntime(ctx context.Context, runtime *enrollmentRuntime) error {
	if runtime == nil {
		return nil
	}
	return errors.Join(runtime.server.Shutdown(ctx), runtime.listener.Close())
}

func decodeCallbackKey(value string) ([]byte, error) {
	key, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(key) != callbacktoken.KeyBytes {
		return nil, errors.New("invalid enrollment callback key")
	}
	return key, nil
}
