package consensus

import (
	"crypto/x509"
	"errors"
	"net"
	"testing"

	"github.com/hashicorp/raft"
)

func TestRevocableConnectionRejectsExistingStreamAfterMembershipRevocation(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	resolver := NewStaticPeerResolver()
	resolver.ApproveNodeID("peer")
	connection := &revocablePeerConnection{
		Conn: left, resolver: resolver, nodeID: "peer", certificate: &x509.Certificate{},
	}
	resolver.RevokeNodeID("peer")
	if _, err := connection.Write([]byte("raft")); !errors.Is(err, errRevokedPeerConnection) {
		t.Fatalf("write after revocation error=%v", err)
	}
	if _, err := connection.Read(make([]byte, 1)); !errors.Is(err, errRevokedPeerConnection) {
		t.Fatalf("read after revocation error=%v", err)
	}
}

func TestRevocableConnectionRejectsExistingStreamAfterFingerprintRotation(t *testing.T) {
	left, right := net.Pipe()
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	resolver := &fingerprintResolver{approved: true, authorized: true}
	connection := &revocablePeerConnection{
		Conn: left, resolver: resolver, nodeID: "peer", certificate: &x509.Certificate{},
	}
	resolver.authorized = false
	if _, err := connection.Write([]byte("raft")); !errors.Is(err, errRevokedPeerConnection) {
		t.Fatalf("write after rotation error=%v", err)
	}
}

type fingerprintResolver struct {
	approved   bool
	authorized bool
}

func (*fingerprintResolver) ExpectedNodeID(_ raft.ServerAddress) (string, bool) {
	return "peer", true
}

func (r *fingerprintResolver) IsApprovedNodeID(string) bool { return r.approved }

func (r *fingerprintResolver) AuthorizeNodeCertificate(string, *x509.Certificate) bool {
	return r.authorized
}
