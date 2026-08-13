package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
)

const membershipChangeTimeout = 30 * time.Second

const membershipReconcileInterval = 3 * time.Second

func maintainConfiguredMembership(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
) {
	ticker := time.NewTicker(membershipReconcileInterval)
	defer ticker.Stop()
	leadership := node.LeadershipChanges()
	converged := node.IsLeader()
	for {
		select {
		case <-ctx.Done():
			return
		case becameLeader := <-leadership:
			if !becameLeader {
				converged = false
			}
		case <-ticker.C:
		}
		if converged || !node.IsLeader() {
			continue
		}
		if err := reconcileConfiguredVoters(ctx, node, nodeConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Raft membership reconciliation: %v\n", err)
			continue
		}
		if err := registerConfiguredNodes(ctx, node, nodeConfig); err != nil {
			fmt.Fprintf(os.Stderr, "Raft node registration: %v\n", err)
			continue
		}
		converged = true
	}
}

func configurePeerResolver(resolver *consensus.StaticPeerResolver, nodeConfig config.Config) {
	setPeerResolverAddresses(resolver, nodeConfig.RaftAdvertise, nodeConfig.NodeID)
	resolver.ApproveNodeID(nodeConfig.NodeID)
	for _, peer := range nodeConfig.RaftPeers {
		setPeerResolverAddresses(resolver, peer.Address, peer.NodeID)
		resolver.ApproveNodeID(peer.NodeID)
	}
}

func setPeerResolverAddresses(
	resolver *consensus.StaticPeerResolver,
	address string,
	nodeID string,
) {
	resolver.Set(raft.ServerAddress(address), nodeID)
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return
	}
	addresses, err := net.LookupHost(host)
	if err != nil {
		return
	}
	for _, resolved := range addresses {
		resolver.Set(raft.ServerAddress(net.JoinHostPort(resolved, port)), nodeID)
	}
}

func reconcileConfiguredVoters(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
) error {
	if len(nodeConfig.RaftPeers) == 0 {
		return nil
	}
	for _, peer := range orderedPeers(nodeConfig) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := node.EnsureVoter(peer.NodeID, peer.Address, membershipChangeTimeout); err != nil {
			return fmt.Errorf("ensure Raft voter %s: %w", peer.NodeID, err)
		}
		fmt.Printf("Raft voter ready: %s at %s\n", peer.NodeID, peer.Address)
	}
	configuration, err := node.Configuration()
	if err != nil {
		return fmt.Errorf("read reconciled Raft configuration: %w", err)
	}
	if len(configuration.Servers) != len(nodeConfig.RaftPeers) {
		return fmt.Errorf(
			"Raft voter count is %d, want %d", len(configuration.Servers), len(nodeConfig.RaftPeers),
		)
	}
	fmt.Printf("Raft membership ready: %d voters\n", len(configuration.Servers))
	return nil
}

func orderedPeers(nodeConfig config.Config) []config.RaftPeer {
	peers := make([]config.RaftPeer, 0, len(nodeConfig.RaftPeers))
	for _, peer := range nodeConfig.RaftPeers {
		if peer.NodeID == nodeConfig.NodeID {
			peers = append(peers, peer)
		}
	}
	for _, peer := range nodeConfig.RaftPeers {
		if peer.NodeID != nodeConfig.NodeID {
			peers = append(peers, peer)
		}
	}
	return peers
}

func registerConfiguredNodes(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
) error {
	for _, peer := range nodeConfig.RaftPeers {
		desired, err := configuredDomainNode(nodeConfig, peer)
		if err != nil {
			return err
		}
		existing, exists := node.State().State().Nodes[domain.NodeID(peer.NodeID)]
		operationID, err := newOperationID()
		if err != nil {
			return err
		}
		kind := clusterstate.CommandAddNode
		if exists {
			if existing.Network == desired.Network {
				continue
			}
			kind = clusterstate.CommandUpdateNodeMetadata
		}
		command, err := clusterstate.NewCommand(operationID, kind, time.Now(), desired)
		if err != nil {
			return err
		}
		result, err := node.Apply(ctx, command)
		if err != nil {
			return fmt.Errorf("register configured node %s: %w", peer.NodeID, err)
		}
		if err := result.Err(); err != nil {
			return fmt.Errorf("register configured node %s: %w", peer.NodeID, err)
		}
	}
	return grantBootstrapOwnerConfiguredNodes(ctx, node, nodeConfig)
}

func configuredDomainNode(nodeConfig config.Config, peer config.RaftPeer) (domain.Node, error) {
	controlAddress, err := peer.EffectiveControlAddress()
	if err != nil {
		return domain.Node{}, err
	}
	enrollmentAddress := ""
	if peer.NodeID == nodeConfig.EffectiveEnrollmentIssuerID() {
		enrollmentAddress, err = nodeConfig.EnrollmentAdvertiseAddress()
		if err != nil {
			return domain.Node{}, err
		}
	}
	return domain.Node{
		ID: domain.NodeID(peer.NodeID), Name: peer.NodeName, Status: domain.NodeOffline,
		Lifecycle: domain.NodeActive,
		Network: domain.NodeNetwork{
			RaftAddress: peer.Address, ControlAddress: controlAddress,
			EnrollmentAddress: enrollmentAddress,
		},
	}, nil
}

func grantBootstrapOwnerConfiguredNodes(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
) error {
	return ensureBootstrapOwner(ctx, node, nodeConfig)
}

func ensureBootstrapOwner(
	ctx context.Context,
	node *consensus.Node,
	nodeConfig config.Config,
) error {
	if nodeConfig.BootstrapOwnerID <= 0 {
		return nil
	}
	ownerID := domain.UserID(nodeConfig.BootstrapOwnerID)
	state := node.State().State()
	access := state.Users[ownerID]
	changed := state.OwnerID() != ownerID || len(state.Users) != 1 ||
		access.Role != domain.RoleOwner || len(access.AllowedNodes) != len(state.Nodes)
	if !changed {
		for nodeID := range state.Nodes {
			if !access.AllowedNodes[nodeID] {
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	operationID, err := newOperationID()
	if err != nil {
		return err
	}
	command, err := clusterstate.NewCommand(
		operationID, clusterstate.CommandSetSoleOwner, time.Now(),
		clusterstate.SetSoleOwner{UserID: ownerID},
	)
	if err != nil {
		return err
	}
	result, err := node.Apply(ctx, command)
	if err != nil {
		return fmt.Errorf("grant bootstrap owner configured nodes: %w", err)
	}
	return result.Err()
}
