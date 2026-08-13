// Package speechsetup provisions the local speech engine used by a Bria node.
package speechsetup

import (
	"context"
	"time"
)

type Phase string

const (
	PhaseMissing            Phase = "missing"
	PhaseInstalling         Phase = "installing"
	PhasePermissionRequired Phase = "permission_required"
	PhaseReady              Phase = "ready"
	PhaseFailed             Phase = "failed"
)

type Request struct {
	NodeID string `json:"node_id"`
}

type Status struct {
	NodeID    string    `json:"node_id"`
	Engine    string    `json:"engine"`
	Phase     Phase     `json:"phase"`
	Detail    string    `json:"detail,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s Status) Validate(nodeID string) bool {
	if s.NodeID != nodeID || (s.Engine != "whisper" && s.Engine != "apple") {
		return false
	}
	switch s.Phase {
	case PhaseMissing, PhaseInstalling, PhasePermissionRequired, PhaseReady, PhaseFailed:
		return true
	default:
		return false
	}
}

type Service interface {
	Start(context.Context, Request) (Status, error)
	Status(context.Context, Request) (Status, error)
}
