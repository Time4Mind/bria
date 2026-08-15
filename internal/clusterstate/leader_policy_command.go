package clusterstate

import "github.com/Time4Mind/bria/internal/domain"

const (
	CommandSetLeaderSelectionMode CommandKind = "set_leader_selection_mode"
	CommandSetPreferredLeader     CommandKind = "set_preferred_leader"
)

type SetLeaderSelectionMode struct {
	Mode domain.LeaderSelectionMode `json:"mode"`
}

type SetPreferredLeader struct {
	NodeID domain.NodeID `json:"node_id"`
}
