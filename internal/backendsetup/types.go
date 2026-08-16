// Package backendsetup installs supported provider CLIs into a node-local,
// user-owned prefix.
package backendsetup

import (
	"context"
	"time"
)

type Phase string

const (
	PhaseMissing    Phase = "missing"
	PhaseInstalling Phase = "installing"
	PhaseReady      Phase = "ready"
	PhaseFailed     Phase = "failed"
)

type Request struct {
	NodeID  string `json:"node_id"`
	Backend string `json:"backend"`
}

type Status struct {
	NodeID    string    `json:"node_id"`
	Backend   string    `json:"backend"`
	Phase     Phase     `json:"phase"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s Status) Validate(request Request) bool {
	if s.NodeID != request.NodeID || s.Backend != request.Backend {
		return false
	}
	switch s.Phase {
	case PhaseMissing, PhaseInstalling, PhaseReady, PhaseFailed:
		return true
	default:
		return false
	}
}

type Service interface {
	Start(context.Context, Request) (Status, error)
	Status(context.Context, Request) (Status, error)
}
