package consensus

import (
	"crypto/x509"
	"sync"

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

type dialPeerResolver interface {
	DialAddress(raft.ServerAddress) (string, bool)
}

type StaticPeerResolver struct {
	mu            sync.RWMutex
	peers         map[raft.ServerAddress]string
	dialAddresses map[string]string
	approved      map[string]struct{}
	fingerprints  map[string]string
}

func NewStaticPeerResolver() *StaticPeerResolver {
	return &StaticPeerResolver{
		peers:         make(map[raft.ServerAddress]string),
		dialAddresses: make(map[string]string),
		approved:      make(map[string]struct{}),
		fingerprints:  make(map[string]string),
	}
}

func (r *StaticPeerResolver) SetDialAddress(nodeID, address string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if nodeID == "" || address == "" {
		delete(r.dialAddresses, nodeID)
		return
	}
	r.dialAddresses[nodeID] = address
}

func (r *StaticPeerResolver) DialAddress(address raft.ServerAddress) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	nodeID, ok := r.peers[address]
	if !ok {
		return "", false
	}
	if override := r.dialAddresses[nodeID]; override != "" {
		return override, true
	}
	return string(address), true
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
