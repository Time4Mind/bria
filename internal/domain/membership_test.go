package domain_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestEnrollmentConsumesInvitationAndAssignsUniqueName(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Office", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	secret := "one-time-secret"
	invite := domain.EnrollmentInvite{
		ID: "invite-1", SecretHash: domain.HashEnrollmentSecret(secret),
		ExpiresAt: now.Add(30 * time.Minute),
	}
	if err := state.IssueEnrollmentInvite(invite, now); err != nil {
		t.Fatal(err)
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(publicKey)
	request := domain.EnrollmentRequest{
		ID: "request-1", InviteID: invite.ID, NodeID: "beta", Name: "Office",
		Network:     domain.NodeNetwork{RaftAddress: "beta:7946", ControlAddress: "beta:7947"},
		PublicKey:   base64.RawURLEncoding.EncodeToString(publicKey),
		Fingerprint: hex.EncodeToString(fingerprint[:]),
		RequestedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := state.SubmitEnrollment(request, invite.SecretHash, now); err != nil {
		t.Fatal(err)
	}
	pending := state.EnrollmentRequests[request.ID]
	if pending.Name != "Office 2" || pending.Status != domain.EnrollmentPending ||
		pending.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatalf("pending request=%#v", pending)
	}
	if err := state.SubmitEnrollment(request, invite.SecretHash, now); err == nil {
		t.Fatal("single-use invitation was accepted twice")
	}
	if err := state.DecideEnrollment(request.ID, true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	joined := state.Nodes["beta"]
	if joined.Name != "Office 2" || !joined.Enabled() || joined.Status != domain.NodeOffline {
		t.Fatalf("joined node=%#v", joined)
	}
}

func TestEnrollmentRejectsUnroutableAddressAndMismatchedFingerprint(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.EnrollmentRequest{
		ID: "request", NodeID: "node", Name: "Node",
		Network:     domain.NodeNetwork{RaftAddress: "0.0.0.0:7946", ControlAddress: "node:7947"},
		PublicKey:   base64.RawURLEncoding.EncodeToString(publicKey),
		Fingerprint: hex.EncodeToString(make([]byte, sha256.Size)),
		RequestedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := request.Validate(); err == nil {
		t.Fatal("unroutable address and mismatched fingerprint were accepted")
	}
}

func TestDisableEnableAndDeleteLifecycle(t *testing.T) {
	now := time.Unix(2_000_000_000, 0).UTC()
	state := domain.NewState()
	for _, node := range []domain.Node{
		{ID: "alpha", Name: "Alpha", Status: domain.NodeOnline},
		{ID: "beta", Name: "Beta", Status: domain.NodeOnline, Fingerprint: "fp"},
	} {
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeLifecycle("beta", domain.NodeDisabled); err != nil {
		t.Fatal(err)
	}
	if state.Nodes["beta"].Status != domain.NodeOffline {
		t.Fatal("disabled node remained online")
	}
	if err := state.SetNodeLifecycle("alpha", domain.NodeDisabled); err == nil {
		t.Fatal("final active node was disabled")
	}
	if err := state.SetNodeLifecycle("beta", domain.NodeActive); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteDisabledNode("beta", now); err == nil {
		t.Fatal("active node was deleted")
	}
	if err := state.SetNodeLifecycle("beta", domain.NodeDisabled); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteDisabledNode("beta", now); err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Nodes["beta"]; exists {
		t.Fatal("deleted node remained visible")
	}
	if state.NodeTombstones["beta"].Fingerprint != "fp" {
		t.Fatal("identity tombstone missing")
	}
	if err := state.AddNode(domain.Node{ID: "beta", Name: "Again"}); err == nil {
		t.Fatal("deleted identity rejoined without enrollment")
	}
}
