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

// IsVoterAt reports whether the committed Raft configuration contains the
// exact voter identity and address. It is used by administrative convergence
// flows that first publish a desired address through the replicated state.
func (n *Node) IsVoterAt(id, address string) bool {
	if id == "" || address == "" {
		return false
	}
	configuration, err := n.Configuration()
	if err != nil {
		return false
	}
	for _, server := range configuration.Servers {
		if string(server.ID) == id && string(server.Address) == address &&
			server.Suffrage == raft.Voter {
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
