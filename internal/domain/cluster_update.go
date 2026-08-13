package domain

import (
	"fmt"
	"strings"
	"time"
)

type ClusterUpdatePhase string
type NodeUpdatePhase string

const (
	ClusterUpdateRunning   ClusterUpdatePhase = "running"
	ClusterUpdateCompleted ClusterUpdatePhase = "completed"
	ClusterUpdateFailed    ClusterUpdatePhase = "failed"

	NodeUpdatePending    NodeUpdatePhase = "pending"
	NodeUpdateInstalling NodeUpdatePhase = "installing"
	NodeUpdateHealthy    NodeUpdatePhase = "healthy"
	NodeUpdateFailed     NodeUpdatePhase = "failed"
)

type NodeUpdate struct {
	Phase     NodeUpdatePhase `json:"phase"`
	UpdatedAt time.Time       `json:"updated_at"`
	Error     string          `json:"error,omitempty"`
}

type ClusterUpdate struct {
	ID             string                `json:"id"`
	Version        string                `json:"version"`
	ManifestSHA256 string                `json:"manifest_sha256"`
	Phase          ClusterUpdatePhase    `json:"phase"`
	Order          []NodeID              `json:"order"`
	Nodes          map[NodeID]NodeUpdate `json:"nodes"`
	StartedAt      time.Time             `json:"started_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	Error          string                `json:"error,omitempty"`
}

func (u ClusterUpdate) Active() bool { return u.Phase == ClusterUpdateRunning }

func (s *State) BeginClusterUpdate(update ClusterUpdate, at time.Time) error {
	if s.ClusterUpdate != nil && s.ClusterUpdate.Active() {
		return fmt.Errorf("%w: cluster update already running", ErrInvalidState)
	}
	update.ID = strings.TrimSpace(update.ID)
	update.Version = strings.TrimSpace(update.Version)
	update.ManifestSHA256 = strings.ToLower(strings.TrimSpace(update.ManifestSHA256))
	if update.ID == "" || update.Version == "" || len(update.ManifestSHA256) != 64 || len(update.Order) == 0 {
		return fmt.Errorf("%w: invalid cluster update", ErrInvalidState)
	}
	seen := make(map[NodeID]bool, len(update.Order))
	update.Nodes = make(map[NodeID]NodeUpdate, len(update.Order))
	for _, nodeID := range update.Order {
		node, ok := s.Nodes[nodeID]
		if !ok || !node.Enabled() || seen[nodeID] {
			return fmt.Errorf("%w: invalid update node %q", ErrInvalidState, nodeID)
		}
		seen[nodeID] = true
		update.Nodes[nodeID] = NodeUpdate{Phase: NodeUpdatePending, UpdatedAt: at}
	}
	update.Phase = ClusterUpdateRunning
	update.StartedAt, update.UpdatedAt = at, at
	copy := cloneClusterUpdate(update)
	s.ClusterUpdate = &copy
	return nil
}

func (s *State) SetClusterUpdateNode(
	updateID string, nodeID NodeID, phase NodeUpdatePhase, detail string, at time.Time,
) error {
	if s.ClusterUpdate == nil || !s.ClusterUpdate.Active() || s.ClusterUpdate.ID != updateID {
		return fmt.Errorf("%w: cluster update is not active", ErrInvalidState)
	}
	current, ok := s.ClusterUpdate.Nodes[nodeID]
	if !ok || !validNodeUpdateTransition(current.Phase, phase) {
		return fmt.Errorf("%w: invalid node update transition", ErrInvalidState)
	}
	if len(detail) > 240 {
		detail = detail[:240]
	}
	current.Phase, current.Error, current.UpdatedAt = phase, strings.TrimSpace(detail), at
	s.ClusterUpdate.Nodes[nodeID] = current
	s.ClusterUpdate.UpdatedAt = at
	return nil
}

func validNodeUpdateTransition(from, to NodeUpdatePhase) bool {
	return from == to ||
		(from == NodeUpdatePending && (to == NodeUpdateInstalling || to == NodeUpdateFailed)) ||
		(from == NodeUpdateInstalling && (to == NodeUpdateHealthy || to == NodeUpdateFailed))
}

func (s *State) FinishClusterUpdate(updateID string, failed bool, detail string, at time.Time) error {
	if s.ClusterUpdate == nil || !s.ClusterUpdate.Active() || s.ClusterUpdate.ID != updateID {
		return fmt.Errorf("%w: cluster update is not active", ErrInvalidState)
	}
	if !failed {
		for _, node := range s.ClusterUpdate.Nodes {
			if node.Phase != NodeUpdateHealthy {
				return fmt.Errorf("%w: update contains unfinished nodes", ErrInvalidState)
			}
		}
		s.ClusterUpdate.Phase = ClusterUpdateCompleted
	} else {
		s.ClusterUpdate.Phase = ClusterUpdateFailed
	}
	if len(detail) > 240 {
		detail = detail[:240]
	}
	s.ClusterUpdate.Error = strings.TrimSpace(detail)
	s.ClusterUpdate.UpdatedAt = at
	return nil
}

func cloneClusterUpdate(update ClusterUpdate) ClusterUpdate {
	update.Order = append([]NodeID(nil), update.Order...)
	update.Nodes = mapsClone(update.Nodes)
	return update
}

func mapsClone[K comparable, V any](source map[K]V) map[K]V {
	result := make(map[K]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
