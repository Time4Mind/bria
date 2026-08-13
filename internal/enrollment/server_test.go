package enrollment_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/enrollment"
	"github.com/Time4Mind/bria/internal/security"
)

type directSubmitter struct {
	machine *clusterstate.Machine
	now     time.Time
}

func (s directSubmitter) SubmitEnrollment(
	_ context.Context,
	request domain.EnrollmentRequest,
	expectedHash string,
) error {
	command, err := clusterstate.NewCommand(
		"submit-"+request.ID, clusterstate.CommandSubmitEnrollment, s.now,
		clusterstate.SubmitEnrollment{Request: request, ExpectedHash: expectedHash},
	)
	if err != nil {
		return err
	}
	return s.machine.Apply(command).Err()
}

func TestInvitationEnrollmentWaitsForApprovalBeforeIssuingCertificate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ca, caPEM, _, err := security.GenerateCA("cluster", now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := security.IssueNodeCertificate(ca, "cluster", "alpha", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tlsCertificate, err := tls.X509KeyPair(issuer.CertificatePEM, issuer.PrivateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "alpha", Name: "Alpha", Status: domain.NodeOnline,
		Network: domain.NodeNetwork{RaftAddress: "alpha:7946", ControlAddress: "alpha:7947"},
	}); err != nil {
		t.Fatal(err)
	}
	secret := "join-secret"
	invite := domain.EnrollmentInvite{
		ID: "invite", SecretHash: domain.HashEnrollmentSecret(secret),
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := state.IssueEnrollmentInvite(invite, now); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := enrollment.NewServer(enrollment.ServerConfig{
		ClusterID: "cluster", IssuerNodeID: "alpha", EnrollmentAddress: listener.Addr().String(),
		Certificate: tlsCertificate, CA: ca, CAPEM: caPEM,
		CallbackKey: make([]byte, 32), State: machine,
		Submit: directSubmitter{machine: machine, now: now}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		<-serveDone
	})
	clusterInvite := security.ClusterInvitation{
		Version: 1, ClusterID: "cluster", IssuerNodeID: "alpha",
		Endpoint: listener.Addr().String(), TokenID: invite.ID, Secret: secret,
		CACertificate: string(caPEM), ExpiresAt: invite.ExpiresAt,
	}
	client, err := enrollment.NewClient(clusterInvite, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := security.SignNodeContract(security.NodeContract{
		RequestID: "request", NodeID: "beta", Name: "Alpha",
		Network: domain.NodeNetwork{RaftAddress: "beta:7946", ControlAddress: "beta:7947"},
		OS:      "linux", Arch: "arm64", ExpiresAt: invite.ExpiresAt,
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.Register(context.Background(), clusterInvite, contract)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := client.Status(context.Background(), registered.RequestID, privateKey)
	if err != nil || pending.Status != domain.EnrollmentPending || pending.Bundle != nil {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	approve, err := clusterstate.NewCommand(
		"approve", clusterstate.CommandDecideEnrollment, now.Add(time.Minute),
		clusterstate.DecideEnrollment{RequestID: registered.RequestID, Approve: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Apply(approve).Err(); err != nil {
		t.Fatal(err)
	}
	approved, err := client.Status(context.Background(), registered.RequestID, privateKey)
	if err != nil || approved.Status != domain.EnrollmentApproved || approved.Bundle == nil {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	issued, err := security.ParseCertificate([]byte(approved.Bundle.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	if !issued.PublicKey.(ed25519.PublicKey).Equal(publicKey) {
		t.Fatal("issued certificate does not bind the joining node key")
	}
	callbackKey, err := base64.RawURLEncoding.DecodeString(approved.Bundle.CallbackKey)
	if err != nil || len(callbackKey) != 32 {
		t.Fatal("invalid callback key bundle")
	}
	if got := machine.State().Nodes["beta"].Name; got != "Alpha 2" {
		t.Fatalf("automatic unique name=%q", got)
	}
}
