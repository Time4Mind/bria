package consensus

import (
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/raft"
)

// IsMember checks the authoritative Raft configuration. Domain registration
// alone is not proof that a certificate still belongs to the live cluster.
func (n *Node) IsMember(id string) bool {
	if id == "" {
		return false
	}
	configuration, err := n.Configuration()
	if err != nil {
		return false
	}
	for _, server := range configuration.Servers {
		if string(server.ID) == id {
			return true
		}
	}
	return false
}

// Configuration returns an immutable point-in-time copy of the latest Raft
// membership configuration.
func (n *Node) Configuration() (raft.Configuration, error) {
	future := n.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return raft.Configuration{}, fmt.Errorf("read raft configuration: %w", err)
	}
	configuration := future.Configuration()
	configuration = configuration.Clone()
	return configuration, nil
}

// IsMemberAt reports whether the committed Raft configuration contains the
// exact member identity and address, regardless of suffrage. It is used by
// administrative convergence flows that first publish a desired address
// through the replicated state.
func (n *Node) IsMemberAt(id, address string) bool {
	if id == "" || address == "" {
		return false
	}
	configuration, err := n.Configuration()
	if err != nil {
		return false
	}
	for _, server := range configuration.Servers {
		if string(server.ID) == id && string(server.Address) == address {
			return true
		}
	}
	return false
}

// EnsureVoter converges one server at a time to an exact voter membership.
// Hashicorp Raft v1.7.3 does not populate ConfigurationFuture.Index, so this
// serializes local callers and repeats validation immediately before AddVoter.
// Raft's leader-only configuration queue serializes the actual cluster change.
func (n *Node) EnsureVoter(id, address string, timeout time.Duration) error {
	n.membershipMu.Lock()
	defer n.membershipMu.Unlock()
	if !n.IsLeader() {
		return raft.ErrNotLeader
	}
	if id == "" || address == "" {
		return errors.New("voter id and address are required")
	}
	configuration, err := n.Configuration()
	if err != nil {
		return err
	}
	wantedID := raft.ServerID(id)
	wantedAddress := raft.ServerAddress(address)
	exact, err := inspectVoter(configuration, wantedID, wantedAddress)
	if err != nil || exact {
		return err
	}
	configuration, err = n.Configuration()
	if err != nil {
		return err
	}
	exact, err = inspectVoter(configuration, wantedID, wantedAddress)
	if err != nil || exact {
		return err
	}
	if err := n.raft.AddVoter(wantedID, wantedAddress, 0, timeout).Error(); err != nil {
		return fmt.Errorf("ensure raft voter %q at %q: %w", id, address, err)
	}
	return nil
}

// EnsureNonvoter adds a replication-only member or updates its address. A
// voter must be demoted explicitly first so callers cannot accidentally remove
// a vote while they only intended to refresh membership metadata.
func (n *Node) EnsureNonvoter(id, address string, timeout time.Duration) error {
	n.membershipMu.Lock()
	defer n.membershipMu.Unlock()
	if !n.IsLeader() {
		return raft.ErrNotLeader
	}
	if id == "" || address == "" {
		return errors.New("nonvoter id and address are required")
	}
	configuration, err := n.Configuration()
	if err != nil {
		return err
	}
	wantedID := raft.ServerID(id)
	wantedAddress := raft.ServerAddress(address)
	exact, voter, err := inspectNonvoter(configuration, wantedID, wantedAddress)
	if err != nil || exact {
		return err
	}
	if voter {
		return fmt.Errorf("raft server %q must be demoted before ensuring nonvoter membership", id)
	}
	if err := n.raft.AddNonvoter(wantedID, wantedAddress, 0, timeout).Error(); err != nil {
		return fmt.Errorf("ensure raft nonvoter %q at %q: %w", id, address, err)
	}
	return nil
}

// DemoteVoter keeps a member as a log-replicating nonvoter while removing its
// election and commit vote.
func (n *Node) DemoteVoter(id string, timeout time.Duration) error {
	n.membershipMu.Lock()
	defer n.membershipMu.Unlock()
	if !n.IsLeader() {
		return raft.ErrNotLeader
	}
	if id == "" {
		return errors.New("voter id is required")
	}
	if err := n.raft.DemoteVoter(raft.ServerID(id), 0, timeout).Error(); err != nil {
		return fmt.Errorf("demote raft voter %q: %w", id, err)
	}
	return nil
}

func inspectVoter(
	configuration raft.Configuration,
	wantedID raft.ServerID,
	wantedAddress raft.ServerAddress,
) (bool, error) {
	for _, server := range configuration.Servers {
		if server.Address == wantedAddress && server.ID != wantedID {
			return false, fmt.Errorf(
				"raft address %q is already assigned to server %q",
				wantedAddress,
				server.ID,
			)
		}
		if server.ID == wantedID &&
			server.Address == wantedAddress &&
			server.Suffrage == raft.Voter {
			return true, nil
		}
	}
	return false, nil
}

func inspectNonvoter(
	configuration raft.Configuration,
	wantedID raft.ServerID,
	wantedAddress raft.ServerAddress,
) (exact bool, voter bool, err error) {
	for _, server := range configuration.Servers {
		if server.Address == wantedAddress && server.ID != wantedID {
			return false, false, fmt.Errorf(
				"raft address %q is already assigned to server %q",
				wantedAddress,
				server.ID,
			)
		}
		if server.ID != wantedID {
			continue
		}
		if server.Suffrage == raft.Voter || server.Suffrage == raft.Staging {
			return false, true, nil
		}
		return server.Address == wantedAddress, false, nil
	}
	return false, false, nil
}
