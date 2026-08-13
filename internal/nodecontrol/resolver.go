package nodecontrol

import (
	"sync"

	"github.com/Time4Mind/bria/internal/domain"
)

type StaticResolver struct {
	mu        sync.RWMutex
	addresses map[string]string
}

type StateResolver struct {
	reader   StateReader
	fallback *StaticResolver
}

// LocalResolver keeps a node's own control traffic on its listener address.
// A node may advertise a tunnel address that is reachable by peers but not by
// itself (for example an SSH reverse-forward on another host).
type LocalResolver struct {
	nodeID   string
	address  string
	delegate Resolver
}

func NewLocalResolver(nodeID, address string, delegate Resolver) *LocalResolver {
	return &LocalResolver{nodeID: nodeID, address: address, delegate: delegate}
}

func (r *LocalResolver) ControlAddress(nodeID string) (string, bool) {
	if r != nil && nodeID == r.nodeID && r.address != "" {
		return r.address, true
	}
	if r == nil || r.delegate == nil {
		return "", false
	}
	return r.delegate.ControlAddress(nodeID)
}

func (r *LocalResolver) NodeFingerprint(nodeID string) (string, bool) {
	if r == nil || r.delegate == nil {
		return "", false
	}
	delegate, ok := r.delegate.(CertificateResolver)
	if !ok {
		return "", false
	}
	return delegate.NodeFingerprint(nodeID)
}

func NewStateResolver(reader StateReader, fallback *StaticResolver) *StateResolver {
	return &StateResolver{reader: reader, fallback: fallback}
}

func (r *StateResolver) ControlAddress(nodeID string) (string, bool) {
	if r != nil && r.reader != nil {
		state := r.reader.State()
		if state != nil {
			if node, ok := state.Nodes[domain.NodeID(nodeID)]; ok {
				if !node.Enabled() {
					return "", false
				}
				if node.Network.ControlAddress != "" {
					return node.Network.ControlAddress, true
				}
				// Snapshots produced before node-control addresses were replicated
				// still contain active nodes with an empty value. Preserve their
				// explicitly configured address until a heartbeat upgrades state.
				if r.fallback != nil {
					return r.fallback.ControlAddress(nodeID)
				}
				return "", false
			}
			if _, deleted := state.NodeTombstones[domain.NodeID(nodeID)]; deleted {
				return "", false
			}
		}
	}
	if r == nil || r.fallback == nil {
		return "", false
	}
	return r.fallback.ControlAddress(nodeID)
}

func (r *StateResolver) NodeFingerprint(nodeID string) (string, bool) {
	if r == nil || r.reader == nil {
		return "", false
	}
	state := r.reader.State()
	if state == nil {
		return "", false
	}
	node, ok := state.Nodes[domain.NodeID(nodeID)]
	return node.Fingerprint, ok && node.Enabled() && node.Fingerprint != ""
}

func NewStaticResolver(addresses map[string]string) *StaticResolver {
	copyAddresses := make(map[string]string, len(addresses))
	for nodeID, address := range addresses {
		if nodeID != "" && address != "" {
			copyAddresses[nodeID] = address
		}
	}
	return &StaticResolver{addresses: copyAddresses}
}

func (r *StaticResolver) ControlAddress(nodeID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	address, ok := r.addresses[nodeID]
	return address, ok
}

func (r *StaticResolver) Set(nodeID, address string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if nodeID == "" || address == "" {
		delete(r.addresses, nodeID)
		return
	}
	r.addresses[nodeID] = address
}
