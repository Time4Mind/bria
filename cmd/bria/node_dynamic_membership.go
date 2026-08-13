package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
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
	// available. Track them from the first pass so a relocated voter can retire
	// a stale bootstrap address after restart without losing its node-local dial
	// override (the override is keyed by node ID, not address).
	for _, peer := range nodeConfig.RaftPeers {
		knownAddresses[domain.NodeID(peer.NodeID)] = peer.Address
	}
	for {
		state := node.State().State()
		configured, err := configuredVoterAddresses(node)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Raft membership read: %v\n", err)
		} else {
			syncMembershipResolver(resolver, state, knownAddresses, configured)
		}
		if err == nil && node.IsLeader() {
			if err := reconcileDesiredVoters(node, state); err != nil {
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
	// entry commits. Revoking it earlier can strand a two-voter cluster because
	// the old configuration still needs both acknowledgements.
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

func configuredVoterAddresses(node *consensus.Node) (map[string]string, error) {
	configuration, err := node.Configuration()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(configuration.Servers))
	for _, server := range configuration.Servers {
		if server.Suffrage == raft.Voter {
			result[string(server.ID)] = string(server.Address)
		}
	}
	return result, nil
}

func reconcileDesiredVoters(node *consensus.Node, state *domain.State) error {
	configuration, err := node.Configuration()
	if err != nil {
		return err
	}
	configured := make(map[string]string, len(configuration.Servers))
	for _, server := range configuration.Servers {
		configured[string(server.ID)] = string(server.Address)
	}
	nodeIDs := make([]string, 0, len(state.Nodes))
	for nodeID := range state.Nodes {
		nodeIDs = append(nodeIDs, string(nodeID))
	}
	sort.Strings(nodeIDs)
	for _, rawID := range nodeIDs {
		nodeID, desired := domain.NodeID(rawID), state.Nodes[domain.NodeID(rawID)]
		address, exists := configured[rawID]
		if !desired.Enabled() || desired.Network.RaftAddress == "" ||
			(exists && address == desired.Network.RaftAddress) {
			continue
		}
		if err := node.EnsureVoter(string(nodeID), desired.Network.RaftAddress,
			membershipChangeTimeout); err != nil {
			return err
		}
		return nil
	}
	for _, server := range configuration.Servers {
		desired, exists := state.Nodes[domain.NodeID(server.ID)]
		if exists && desired.Enabled() {
			continue
		}
		if string(server.ID) == node.LeaderID() {
			if target := firstEnabledVoter(state, configured, string(server.ID)); target != "" {
				return node.TransferLeadershipTo(target)
			}
			return nil
		}
		if err := node.RemoveServer(string(server.ID), membershipChangeTimeout); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func firstEnabledVoter(state *domain.State, configured map[string]string, except string) string {
	nodeIDs := make([]string, 0, len(configured))
	for nodeID := range configured {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		candidate, exists := state.Nodes[domain.NodeID(nodeID)]
		if nodeID != except && exists && candidate.Enabled() {
			return nodeID
		}
	}
	return ""
}
