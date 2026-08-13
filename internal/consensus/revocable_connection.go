package consensus

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
)

var errRevokedPeerConnection = errors.New("Raft peer identity was revoked")

// revocablePeerConnection rechecks replicated admission on active streams.
// TLS authentication at handshake time alone is insufficient: a disabled node
// or a rotated old key must not keep an already-open Raft channel indefinitely.
type revocablePeerConnection struct {
	net.Conn
	resolver    PeerResolver
	nodeID      string
	certificate *x509.Certificate
}

func (l *TLSStreamLayer) revocableConnection(connection *tls.Conn, nodeID string) net.Conn {
	return &revocablePeerConnection{
		Conn: connection, resolver: l.resolver, nodeID: nodeID,
		certificate: connection.ConnectionState().PeerCertificates[0],
	}
}

func (c *revocablePeerConnection) Read(buffer []byte) (int, error) {
	if !c.authorized() {
		return 0, errRevokedPeerConnection
	}
	return c.Conn.Read(buffer)
}

func (c *revocablePeerConnection) Write(buffer []byte) (int, error) {
	if !c.authorized() {
		return 0, errRevokedPeerConnection
	}
	return c.Conn.Write(buffer)
}

func (c *revocablePeerConnection) authorized() bool {
	if c.resolver == nil || !c.resolver.IsApprovedNodeID(c.nodeID) {
		return false
	}
	resolver, ok := c.resolver.(certificatePeerResolver)
	return !ok || resolver.AuthorizeNodeCertificate(c.nodeID, c.certificate)
}
