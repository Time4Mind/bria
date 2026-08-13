package domain

import "time"

type NodeStatus string

type NodeLifecycle string

const (
	NodeOnline       NodeStatus = "online"
	NodeReconnecting NodeStatus = "reconnecting"
	NodeOffline      NodeStatus = "offline"

	NodeActive   NodeLifecycle = "active"
	NodeDisabled NodeLifecycle = "disabled"
)

type NodeNetwork struct {
	RaftAddress       string `json:"raft_address"`
	ControlAddress    string `json:"control_address"`
	EnrollmentAddress string `json:"enrollment_address,omitempty"`
}

type BackendDescriptor struct {
	Name         string   `json:"name"`
	Version      string   `json:"version,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type TranscriptFinalReport struct {
	SessionID  SessionID `json:"session_id"`
	Generation uint64    `json:"generation"`
	Timestamp  time.Time `json:"timestamp"`
	Digest     string    `json:"digest"`
}

type Node struct {
	ID      NodeID     `json:"id"`
	Name    string     `json:"name"`
	Status  NodeStatus `json:"status"`
	BootID  string     `json:"boot_id,omitempty"`
	Version string     `json:"version,omitempty"`
	// InstalledBackends is the read-only host inventory. Backends contains only
	// providers the owner explicitly connected to Bria on this node.
	InstalledBackends           []BackendDescriptor `json:"installed_backends,omitempty"`
	Backends                    []BackendDescriptor `json:"backends,omitempty"`
	BackendSelectionInitialized bool                `json:"backend_selection_initialized,omitempty"`
	CreatedAt                   time.Time           `json:"created_at,omitempty"`
	LastSeenAt                  time.Time           `json:"last_seen_at,omitempty"`
	LastEventSeq                uint64              `json:"last_event_sequence,omitempty"`
	Lifecycle                   NodeLifecycle       `json:"lifecycle,omitempty"`
	Network                     NodeNetwork         `json:"network,omitempty"`
	OS                          string              `json:"os,omitempty"`
	Arch                        string              `json:"arch,omitempty"`
	Fingerprint                 string              `json:"fingerprint,omitempty"`
}

func (n Node) EffectiveLifecycle() NodeLifecycle {
	if n.Lifecycle == "" {
		return NodeActive
	}
	return n.Lifecycle
}

func (n Node) Enabled() bool { return n.EffectiveLifecycle() == NodeActive }
