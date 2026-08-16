package domain

import "fmt"

type LeaderSelectionMode string

const (
	LeaderSelectionManual    LeaderSelectionMode = "manual"
	LeaderSelectionAutomatic LeaderSelectionMode = "automatic"
)

// LeaderPolicy controls which consensus member may expose interactive adapters.
// Manual is deliberately the zero/default mode for snapshots created before the
// policy existed. Membership reconciliation makes that member the sole voter;
// automatic mode restores the ordinary all-voter Raft topology.
type LeaderPolicy struct {
	Mode   LeaderSelectionMode `json:"mode,omitempty"`
	NodeID NodeID              `json:"node_id,omitempty"`
}

func (p LeaderPolicy) EffectiveMode() LeaderSelectionMode {
	if p.Mode == LeaderSelectionAutomatic {
		return LeaderSelectionAutomatic
	}
	return LeaderSelectionManual
}

func (s *State) SetLeaderSelectionMode(mode LeaderSelectionMode) error {
	if mode != LeaderSelectionManual && mode != LeaderSelectionAutomatic {
		return fmt.Errorf("%w: unsupported leader selection mode", ErrInvalidState)
	}
	s.LeaderPolicy.Mode = mode
	return nil
}

func (s *State) SetPreferredLeader(nodeID NodeID) error {
	node, ok := s.Nodes[nodeID]
	if !ok || !node.Enabled() {
		return ErrNotFound
	}
	s.LeaderPolicy.NodeID = nodeID
	return nil
}
