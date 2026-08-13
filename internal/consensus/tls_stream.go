package consensus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/security"
	"github.com/hashicorp/raft"
)

type PeerResolver interface {
	ExpectedNodeID(address raft.ServerAddress) (string, bool)
	IsApprovedNodeID(nodeID string) bool
}

type certificatePeerResolver interface {
	AuthorizeNodeCertificate(string, *x509.Certificate) bool
}

type StaticPeerResolver struct {
	mu           sync.RWMutex
	peers        map[raft.ServerAddress]string
	approved     map[string]struct{}
	fingerprints map[string]string
}

func NewStaticPeerResolver() *StaticPeerResolver {
	return &StaticPeerResolver{
		peers:        make(map[raft.ServerAddress]string),
		approved:     make(map[string]struct{}),
		fingerprints: make(map[string]string),
	}
}

func (r *StaticPeerResolver) Set(address raft.ServerAddress, nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[address] = nodeID
}

func (r *StaticPeerResolver) Delete(address raft.ServerAddress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.peers, address)
}

func (r *StaticPeerResolver) DeleteAddress(address string) {
	r.Delete(raft.ServerAddress(address))
}

func (r *StaticPeerResolver) ExpectedNodeID(address raft.ServerAddress) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodeID, ok := r.peers[address]
	return nodeID, ok
}

// ApproveNodeID allows a CA-authenticated node identity to establish inbound
// Raft connections. Address resolution and inbound admission are deliberately
// separate: learning a dial address must never grant cluster membership.
func (r *StaticPeerResolver) ApproveNodeID(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if nodeID != "" {
		r.approved[nodeID] = struct{}{}
	}
}

func (r *StaticPeerResolver) RevokeNodeID(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.approved, nodeID)
	delete(r.fingerprints, nodeID)
}

func (r *StaticPeerResolver) SetNodeFingerprint(nodeID string, fingerprint string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if nodeID == "" {
		return
	}
	if fingerprint == "" {
		delete(r.fingerprints, nodeID)
		return
	}
	r.fingerprints[nodeID] = fingerprint
}

func (r *StaticPeerResolver) AuthorizeNodeCertificate(
	nodeID string,
	certificate *x509.Certificate,
) bool {
	r.mu.RLock()
	active := r.fingerprints[nodeID]
	r.mu.RUnlock()
	if active == "" {
		return true
	}
	current, err := security.NodeCertificateFingerprint(certificate)
	if err != nil {
		return false
	}
	if current == active {
		return true
	}
	previous, present, err := security.PreviousNodeCertificateFingerprint(certificate)
	return err == nil && present && previous == active
}

func (r *StaticPeerResolver) IsApprovedNodeID(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, approved := r.approved[nodeID]
	return approved
}

type TLSStreamConfig struct {
	Listener         net.Listener
	AdvertiseAddress string
	Certificate      tls.Certificate
	Roots            *x509.CertPool
	ClusterID        string
	LocalNodeID      string
	Resolver         PeerResolver
	HandshakeTimeout time.Duration
	Now              func() time.Time
}

type TLSStreamLayer struct {
	listener         net.Listener
	advertiseAddress net.Addr
	serverConfig     *tls.Config
	clientConfig     *tls.Config
	roots            *x509.CertPool
	clusterID        string
	resolver         PeerResolver
	handshakeTimeout time.Duration
	now              func() time.Time
}

type advertisedAddress string

func (a advertisedAddress) Network() string { return "tcp" }
func (a advertisedAddress) String() string  { return string(a) }

func NewTLSStreamLayer(config TLSStreamConfig) (*TLSStreamLayer, error) {
	if config.Listener == nil || config.Roots == nil || config.Resolver == nil {
		return nil, errors.New("listener, roots, and peer resolver are required")
	}
	if len(config.Certificate.Certificate) == 0 {
		return nil, errors.New("local TLS certificate is required")
	}
	if config.ClusterID == "" || config.LocalNodeID == "" {
		return nil, errors.New("cluster id and local node id are required")
	}
	timeout := config.HandshakeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	layer := &TLSStreamLayer{
		listener:         config.Listener,
		roots:            config.Roots,
		clusterID:        config.ClusterID,
		resolver:         config.Resolver,
		handshakeTimeout: timeout,
		now:              now,
	}
	if config.AdvertiseAddress != "" {
		if err := validateAdvertisedAddress(config.AdvertiseAddress); err != nil {
			return nil, fmt.Errorf("validate advertised raft address: %w", err)
		}
		// Raft persists StreamLayer.Addr verbatim as the server address. Keep a
		// configured hostname intact so a relay or node IP can change without
		// changing the stable cluster identity.
		layer.advertiseAddress = advertisedAddress(config.AdvertiseAddress)
	}
	layer.serverConfig = &tls.Config{
		Certificates: []tls.Certificate{config.Certificate},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			return layer.verifyAnyClusterNode(state, x509.ExtKeyUsageClientAuth)
		},
	}
	// Hostname verification cannot represent a stable node identity when IPs
	// change. The dial path therefore disables only the built-in DNS check and
	// replaces it with CA-chain + exact SPIFFE node-ID verification below.
	layer.clientConfig = &tls.Config{
		Certificates:       []tls.Certificate{config.Certificate},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // custom VerifyConnection is stricter than DNS
	}
	return layer, nil
}

func (l *TLSStreamLayer) Accept() (net.Conn, error) {
	raw, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	connection := tls.Server(raw, l.serverConfig)
	if err := l.handshake(connection); err != nil {
		_ = raw.Close()
		return nil, err
	}
	nodeID, err := security.NodeIDFromCertificate(
		connection.ConnectionState().PeerCertificates[0], l.clusterID,
	)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return l.revocableConnection(connection, nodeID), nil
}

func (l *TLSStreamLayer) Dial(address raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	expectedNodeID, ok := l.resolver.ExpectedNodeID(address)
	if !ok || expectedNodeID == "" {
		return nil, fmt.Errorf("no authenticated identity mapping for peer %q", address)
	}
	dialer := net.Dialer{Timeout: timeout}
	raw, err := dialer.Dial("tcp", string(address))
	if err != nil {
		return nil, err
	}
	clientConfig := l.clientConfig.Clone()
	clientConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("peer sent no certificate")
		}
		if err := security.VerifyNodeCertificate(
			state.PeerCertificates[0],
			l.roots,
			l.clusterID,
			expectedNodeID,
			l.now(),
			x509.ExtKeyUsageServerAuth,
		); err != nil {
			return err
		}
		if resolver, ok := l.resolver.(certificatePeerResolver); ok &&
			!resolver.AuthorizeNodeCertificate(expectedNodeID, state.PeerCertificates[0]) {
			return fmt.Errorf("node identity %q uses a revoked key", expectedNodeID)
		}
		return nil
	}
	connection := tls.Client(raw, clientConfig)
	if err := l.handshake(connection); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return l.revocableConnection(connection, expectedNodeID), nil
}

func (l *TLSStreamLayer) Close() error {
	return l.listener.Close()
}

func (l *TLSStreamLayer) Addr() net.Addr {
	if l.advertiseAddress != nil {
		return l.advertiseAddress
	}
	return l.listener.Addr()
}

func (l *TLSStreamLayer) handshake(connection *tls.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), l.handshakeTimeout)
	defer cancel()
	if err := connection.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("mTLS handshake: %w", err)
	}
	return nil
}

func (l *TLSStreamLayer) verifyAnyClusterNode(
	state tls.ConnectionState,
	usage x509.ExtKeyUsage,
) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("peer sent no certificate")
	}
	certificate := state.PeerCertificates[0]
	nodeID, err := security.NodeIDFromCertificate(certificate, l.clusterID)
	if err != nil {
		return err
	}
	if err := security.VerifyNodeCertificate(
		certificate,
		l.roots,
		l.clusterID,
		nodeID,
		l.now(),
		usage,
	); err != nil {
		return err
	}
	if !l.resolver.IsApprovedNodeID(nodeID) {
		return fmt.Errorf("node identity %q is not approved for inbound Raft connections", nodeID)
	}
	if resolver, ok := l.resolver.(certificatePeerResolver); ok &&
		!resolver.AuthorizeNodeCertificate(nodeID, certificate) {
		return fmt.Errorf("node identity %q uses a revoked key", nodeID)
	}
	return nil
}

var _ raft.StreamLayer = (*TLSStreamLayer)(nil)
