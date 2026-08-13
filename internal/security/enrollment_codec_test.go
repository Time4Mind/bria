package security_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

func TestClusterInvitationRoundTripAndExpiry(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	input := security.ClusterInvitation{
		Version: 1, ClusterID: "cluster", IssuerNodeID: "alpha",
		Endpoint: "alpha:7948", TokenID: "invite", Secret: "secret",
		CACertificate: "certificate", ExpiresAt: now.Add(30 * time.Minute),
	}
	encoded, err := security.EncodeClusterInvitation(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := security.DecodeClusterInvitation(encoded, now)
	if err != nil || decoded.TokenID != input.TokenID {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if _, err := security.DecodeClusterInvitation(encoded, input.ExpiresAt); err == nil {
		t.Fatal("expired invitation accepted")
	}
}

func TestEnrollmentClaimRoundTrip(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	encoded, err := security.EncodeEnrollmentClaim(security.EnrollmentClaim{
		Version: 1, ClusterID: "cluster", IssuerNodeID: "alpha",
		Endpoint: "alpha:7948", RequestID: "request", CACertificate: "certificate",
		ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := security.DecodeEnrollmentClaim(encoded, now)
	if err != nil || claim.RequestID != "request" {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, err := security.DecodeEnrollmentClaim(encoded, now.Add(time.Hour)); err == nil {
		t.Fatal("expired enrollment claim accepted")
	}
}

func TestNodeContractRequiresSignatureAndExpires(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := security.SignNodeContract(security.NodeContract{
		RequestID: "request", NodeID: "beta", Name: "Beta",
		Network: domain.NodeNetwork{RaftAddress: "beta:7946", ControlAddress: "beta:7947"},
		OS:      "linux", Arch: "arm64", ExpiresAt: now.Add(time.Hour),
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := security.DecodeNodeContract(encoded, now)
	if err != nil || decoded.NodeID != "beta" {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	tampered := strings.Replace(encoded, "bria-node1.", "bria-node1.A", 1)
	if _, err := security.DecodeNodeContract(tampered, now); err == nil {
		t.Fatal("tampered contract accepted")
	}
	if _, err := security.DecodeNodeContract(encoded, now.Add(time.Hour)); err == nil {
		t.Fatal("expired contract accepted")
	}
}
