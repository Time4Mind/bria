package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
)

func maintainDynamicMembership(
	ctx context.Context,
	node *consensus.Node,
	resolver *consensus.StaticPeerResolver,
	nodeConfig config.Config,
) {
	ticker := time.NewTicker(membershipReconcileInterval)
	defer ticker.Stop()
	knownAddresses := make(map[domain.NodeID]string)
	// configurePeerResolver seeds these mappings before the replicated state is
	// available. Track them from the first pass so a relocated member can retire
	// a stale bootstrap address after restart without losing its node-local dial
	// override (the override is keyed by node ID, not address).
	for _, peer := range nodeConfig.RaftPeers {
		knownAddresses[domain.NodeID(peer.NodeID)] = peer.Address
	}
	for {
		state := node.State().State()
		configured, err := configuredMemberAddresses(node)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Raft membership read: %v\n", err)
		} else {
			syncMembershipResolverIfReady(resolver, state, knownAddresses, configured)
		}
		if err == nil && node.IsLeader() {
			if err := reconcileDesiredMembership(node, state); err != nil {
				fmt.Fprintf(os.Stderr, "Raft dynamic membership: %v\n", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func syncMembershipResolverIfReady(
	resolver *consensus.StaticPeerResolver,
	state *domain.State,
	known map[domain.NodeID]string,
	configured map[string]string,
) bool {
	// A joining node starts with an empty replicated state and can briefly report
	// an empty Raft configuration before the leader sends its first snapshot.
	// Keep the statically seeded identities during that window; revoking them here
	// rejects the very peer that must deliver the snapshot.
	// A fresh non-bootstrap Raft store can already list the joining node itself
	// before it has received any replicated product state.  That self-only
	// configuration is not evidence that the initial snapshot arrived, so keep
	// all statically seeded peer identities until at least one replicated node
	// record is present.
	if len(state.Nodes) == 0 {
		return false
	}
	syncMembershipResolver(resolver, state, known, configured)
	return true
}

func syncMembershipResolver(
	resolver *consensus.StaticPeerResolver,
	state *domain.State,
	known map[domain.NodeID]string,
	configured map[string]string,
) {
	for nodeID, address := range known {
		nodeState, exists := state.Nodes[nodeID]
		configuredAddress, stillVoter := configured[string(nodeID)]
		moved := exists && nodeState.Enabled() &&
			nodeState.Network.RaftAddress != address && configuredAddress != address
		if ((!exists || !nodeState.Enabled()) && !stillVoter) || moved {
			resolver.DeleteAddress(address)
			resolver.RevokeNodeID(string(nodeID))
			delete(known, nodeID)
		}
	}
	// A disabled member remains authenticated for Raft only until its removal
	// entry commits. Revoking it earlier can strand an automatic two-voter
	// cluster because the old configuration still needs both acknowledgements.
	for nodeID, address := range configured {
		setPeerResolverAddresses(resolver, address, nodeID)
		resolver.ApproveNodeID(nodeID)
		if nodeState, exists := state.Nodes[domain.NodeID(nodeID)]; exists {
			resolver.SetNodeFingerprint(nodeID, nodeState.Fingerprint)
		}
		if _, exists := known[domain.NodeID(nodeID)]; !exists {
			known[domain.NodeID(nodeID)] = address
		}
	}
	for nodeID, nodeState := range state.Nodes {
		if !nodeState.Enabled() || nodeState.Network.RaftAddress == "" {
			continue
		}
		setPeerResolverAddresses(resolver, nodeState.Network.RaftAddress, string(nodeID))
		resolver.ApproveNodeID(string(nodeID))
		resolver.SetNodeFingerprint(string(nodeID), nodeState.Fingerprint)
		if tracked, exists := known[nodeID]; !exists || tracked == nodeState.Network.RaftAddress ||
			configured[string(nodeID)] != tracked {
			known[nodeID] = nodeState.Network.RaftAddress
		}
	}
}

func configuredMemberAddresses(node *consensus.Node) (map[string]string, error) {
	configuration, err := node.Configuration()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(configuration.Servers))
	for _, server := range configuration.Servers {
		result[string(server.ID)] = string(server.Address)
	}
	return result, nil
}
