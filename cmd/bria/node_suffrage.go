package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
)

const manualReplicaParkDelay = 2 * time.Minute

type membershipActionKind uint8

const (
	membershipNoop membershipActionKind = iota
	membershipEnsureVoter
	membershipEnsureNonvoter
	membershipDemoteVoter
	membershipRemoveServer
	membershipTransferLeadership
)

type membershipAction struct {
	kind    membershipActionKind
	nodeID  string
	address string
}

func reconcileDesiredMembership(node *consensus.Node, state *domain.State) error {
	configuration, err := node.Configuration()
	if err != nil {
		return err
	}
	return applyMembershipAction(node, nextMembershipAction(configuration, state, node.LeaderID()))
}

func applyMembershipAction(node *consensus.Node, action membershipAction) error {
	switch action.kind {
	case membershipNoop:
		return nil
	case membershipEnsureVoter:
		return node.EnsureVoter(action.nodeID, action.address, membershipChangeTimeout)
	case membershipEnsureNonvoter:
		return node.EnsureNonvoter(action.nodeID, action.address, membershipChangeTimeout)
	case membershipDemoteVoter:
		return node.DemoteVoter(action.nodeID, membershipChangeTimeout)
	case membershipRemoveServer:
		return node.RemoveServer(action.nodeID, membershipChangeTimeout)
	case membershipTransferLeadership:
		return node.TransferLeadershipTo(action.nodeID)
	default:
		return fmt.Errorf("unsupported membership action %d", action.kind)
	}
}

func nextMembershipAction(
	configuration raft.Configuration,
	state *domain.State,
	leaderID string,
) membershipAction {
	return nextMembershipActionAt(configuration, state, leaderID, time.Now().UTC())
}

func nextMembershipActionAt(
	configuration raft.Configuration,
	state *domain.State,
	leaderID string,
	now time.Time,
) membershipAction {
	servers := indexRaftServers(configuration)
	if state.LeaderPolicy.EffectiveMode() == domain.LeaderSelectionManual {
		return nextManualMembershipAction(state, servers, leaderID, now)
	}
	return nextAutomaticMembershipAction(state, configuration, servers, leaderID)
}

func nextManualMembershipAction(
	state *domain.State,
	servers map[string]raft.Server,
	leaderID string,
	now time.Time,
) membershipAction {
	preferredID := string(state.LeaderPolicy.NodeID)
	if preferredID == "" {
		preferredID = leaderID
	}
	preferred, exists := state.Nodes[domain.NodeID(preferredID)]
	if preferredID == "" || !exists || !preferred.Enabled() || preferred.Network.RaftAddress == "" {
		return membershipAction{}
	}
	server, configured := servers[preferredID]
	if !configured || server.Suffrage != raft.Voter ||
		string(server.Address) != preferred.Network.RaftAddress {
		// Never turn an unreachable replacement into a voter: a two-voter
		// transition would then strand the still-running old leader.
		if preferredID != leaderID && preferred.Status == domain.NodeOffline {
			return membershipAction{}
		}
		return membershipAction{
			kind: membershipEnsureVoter, nodeID: preferredID,
			address: preferred.Network.RaftAddress,
		}
	}
	if leaderID != preferredID {
		if preferred.Status == domain.NodeOffline {
			return membershipAction{}
		}
		return membershipAction{kind: membershipTransferLeadership, nodeID: preferredID}
	}

	for _, server := range orderedManualDemotions(state, servers, preferredID) {
		if string(server.ID) == preferredID || server.Suffrage != raft.Voter {
			continue
		}
		return membershipAction{kind: membershipDemoteVoter, nodeID: string(server.ID)}
	}
	for _, nodeID := range orderedEnabledNodeIDs(state) {
		if nodeID == preferredID {
			continue
		}
		desired := state.Nodes[domain.NodeID(nodeID)]
		if manualReplicaParked(desired, now) {
			continue
		}
		server, configured := servers[nodeID]
		if configured && server.Suffrage == raft.Nonvoter &&
			string(server.Address) == desired.Network.RaftAddress {
			continue
		}
		return membershipAction{
			kind: membershipEnsureNonvoter, nodeID: nodeID,
			address: desired.Network.RaftAddress,
		}
	}
	for _, server := range orderedRaftServers(servers) {
		if string(server.ID) == preferredID || server.Suffrage != raft.Nonvoter {
			continue
		}
		desired, exists := state.Nodes[domain.NodeID(server.ID)]
		if exists && manualReplicaParked(desired, now) {
			return membershipAction{kind: membershipRemoveServer, nodeID: string(server.ID)}
		}
	}
	return nextRemovedMemberAction(state, servers, leaderID)
}

func manualReplicaParked(node domain.Node, now time.Time) bool {
	return node.Enabled() && node.Status == domain.NodeOffline && !node.LastSeenAt.IsZero() &&
		!now.Before(node.LastSeenAt) && now.Sub(node.LastSeenAt) >= manualReplicaParkDelay
}

func nextAutomaticMembershipAction(
	state *domain.State,
	configuration raft.Configuration,
	servers map[string]raft.Server,
	leaderID string,
) membershipAction {
	for _, nodeID := range orderedEnabledNodeIDs(state) {
		desired := state.Nodes[domain.NodeID(nodeID)]
		server, configured := servers[nodeID]
		if configured && server.Suffrage == raft.Voter &&
			string(server.Address) == desired.Network.RaftAddress {
			continue
		}
		return membershipAction{
			kind: membershipEnsureVoter, nodeID: nodeID,
			address: desired.Network.RaftAddress,
		}
	}
	if action := nextRemovedMemberAction(state, servers, leaderID); action.kind != membershipNoop {
		if action.nodeID == leaderID {
			if target := firstEnabledConfiguredVoter(state, configuration, leaderID); target != "" {
				return membershipAction{kind: membershipTransferLeadership, nodeID: target}
			}
			return membershipAction{}
		}
		return action
	}
	return membershipAction{}
}

func nextRemovedMemberAction(
	state *domain.State,
	servers map[string]raft.Server,
	leaderID string,
) membershipAction {
	for _, server := range orderedRaftServers(servers) {
		desired, exists := state.Nodes[domain.NodeID(server.ID)]
		if exists && desired.Enabled() && desired.Network.RaftAddress != "" {
			continue
		}
		if string(server.ID) == leaderID {
			return membershipAction{kind: membershipRemoveServer, nodeID: leaderID}
		}
		return membershipAction{kind: membershipRemoveServer, nodeID: string(server.ID)}
	}
	return membershipAction{}
}

func indexRaftServers(configuration raft.Configuration) map[string]raft.Server {
	servers := make(map[string]raft.Server, len(configuration.Servers))
	for _, server := range configuration.Servers {
		servers[string(server.ID)] = server
	}
	return servers
}

func orderedRaftServers(servers map[string]raft.Server) []raft.Server {
	ids := make([]string, 0, len(servers))
	for nodeID := range servers {
		ids = append(ids, nodeID)
	}
	sort.Strings(ids)
	result := make([]raft.Server, 0, len(ids))
	for _, nodeID := range ids {
		result = append(result, servers[nodeID])
	}
	return result
}

func orderedManualDemotions(
	state *domain.State,
	servers map[string]raft.Server,
	preferredID string,
) []raft.Server {
	result := orderedRaftServers(servers)
	sort.SliceStable(result, func(left, right int) bool {
		return demotionPriority(state, result[left], preferredID) <
			demotionPriority(state, result[right], preferredID)
	})
	return result
}

func demotionPriority(state *domain.State, server raft.Server, preferredID string) int {
	if string(server.ID) == preferredID || server.Suffrage != raft.Voter {
		return 4
	}
	node, exists := state.Nodes[domain.NodeID(server.ID)]
	if !exists || !node.Enabled() || node.Status == domain.NodeOffline {
		return 0
	}
	if node.Status == domain.NodeReconnecting || node.Status == "" {
		return 1
	}
	return 2
}

func orderedEnabledNodeIDs(state *domain.State) []string {
	ids := make([]string, 0, len(state.Nodes))
	for nodeID, node := range state.Nodes {
		if node.Enabled() && node.Network.RaftAddress != "" {
			ids = append(ids, string(nodeID))
		}
	}
	sort.Strings(ids)
	return ids
}

func firstEnabledConfiguredVoter(
	state *domain.State,
	configuration raft.Configuration,
	except string,
) string {
	servers := indexRaftServers(configuration)
	for _, nodeID := range orderedEnabledNodeIDs(state) {
		server, exists := servers[nodeID]
		if nodeID != except && exists && server.Suffrage == raft.Voter {
			return nodeID
		}
	}
	return ""
}
