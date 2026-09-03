package update

import (
	"fmt"
	"sort"
	"strings"
)

type Role string

const (
	RoleExecutor    Role = "executor"
	RoleCoordinator Role = "coordinator"
)

type Availability string

const (
	AvailabilityUnknown Availability = "unknown"
	AvailabilityOffline Availability = "offline"
	AvailabilityOnline  Availability = "online"
)

type NodeState string

const (
	NodePending                NodeState = "pending"
	NodeInstalling             NodeState = "installing"
	NodeAwaitingHealth         NodeState = "awaiting_health"
	NodeRollbackPending        NodeState = "rollback_pending"
	NodeRollingBack            NodeState = "rolling_back"
	NodeAwaitingRollbackHealth NodeState = "awaiting_rollback_health"
	NodeUpdated                NodeState = "updated"
	NodeRolledBack             NodeState = "rolled_back"
	NodeBlocked                NodeState = "blocked"
	NodeRollbackFailed         NodeState = "rollback_failed"
)

type RolloutStatus string

const (
	RolloutRunning        RolloutStatus = "running"
	RolloutStopped        RolloutStatus = "stopped"
	RolloutCompleted      RolloutStatus = "completed"
	RolloutRollbackFailed RolloutStatus = "rollback_failed"
)

type ActionKind string

const (
	ActionInstall  ActionKind = "install"
	ActionRollback ActionKind = "rollback"
)

type Action struct {
	Kind    ActionKind `json:"kind"`
	NodeID  string     `json:"node_id"`
	Version string     `json:"version"`
}

type Node struct {
	ID             string       `json:"id"`
	Role           Role         `json:"role"`
	CurrentVersion string       `json:"current_version"`
	Availability   Availability `json:"availability"`
	State          NodeState    `json:"state"`
	BlockedFrom    NodeState    `json:"blocked_from,omitempty"`
}

// HealthReceipt is a reread of externally observable postflight state. Every
// applicable readiness flag is required; executors must reconnect to the
// coordinator. RunningVersion must exactly equal the requested version.
type HealthReceipt struct {
	NodeID               string `json:"node_id"`
	RunningVersion       string `json:"running_version"`
	Started              bool   `json:"started"`
	StateReadable        bool   `json:"state_readable"`
	ProvidersAvailable   bool   `json:"providers_available"`
	CoordinatorConnected bool   `json:"coordinator_connected"`
	SessionsAvailable    bool   `json:"sessions_available"`
	ProbeSucceeded       bool   `json:"probe_succeeded"`
}

func (receipt HealthReceipt) healthyFor(node Node, version string) bool {
	connected := node.Role == RoleCoordinator || receipt.CoordinatorConnected
	return receipt.NodeID == node.ID && receipt.RunningVersion == version && receipt.Started &&
		receipt.StateReadable && receipt.ProvidersAvailable && connected && receipt.SessionsAvailable &&
		receipt.ProbeSucceeded
}

// Rollout is serializable state. The caller durably saves it before executing
// each returned action and after recording each external receipt.
type Rollout struct {
	TargetVersion string        `json:"target_version"`
	RolloutState  RolloutStatus `json:"status"`
	OrderedNodes  []Node        `json:"nodes"`
	CurrentIndex  int           `json:"current_index"`
}

func NewRollout(targetVersion string, nodes []Node) (*Rollout, error) {
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" || len(nodes) == 0 {
		return nil, fmt.Errorf("%w: target version and nodes are required", ErrInvalidRollout)
	}
	copyNodes := append([]Node(nil), nodes...)
	seen := make(map[string]struct{}, len(copyNodes))
	coordinators := 0
	for index := range copyNodes {
		copyNodes[index].ID = strings.TrimSpace(copyNodes[index].ID)
		copyNodes[index].CurrentVersion = strings.TrimSpace(copyNodes[index].CurrentVersion)
		if copyNodes[index].ID == "" || copyNodes[index].CurrentVersion == "" {
			return nil, fmt.Errorf("%w: node identity and current version are required", ErrInvalidRollout)
		}
		if _, duplicate := seen[copyNodes[index].ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate node %q", ErrInvalidRollout, copyNodes[index].ID)
		}
		seen[copyNodes[index].ID] = struct{}{}
		switch copyNodes[index].Role {
		case RoleExecutor:
		case RoleCoordinator:
			coordinators++
		default:
			return nil, fmt.Errorf("%w: invalid role for %q", ErrInvalidRollout, copyNodes[index].ID)
		}
		switch copyNodes[index].Availability {
		case AvailabilityUnknown, AvailabilityOffline, AvailabilityOnline:
		default:
			return nil, fmt.Errorf("%w: invalid availability for %q", ErrInvalidRollout, copyNodes[index].ID)
		}
		if (copyNodes[index].State != "" && copyNodes[index].State != NodePending) || copyNodes[index].BlockedFrom != "" {
			return nil, fmt.Errorf("%w: new nodes must be pending", ErrInvalidRollout)
		}
		copyNodes[index].State = NodePending
	}
	if coordinators != 1 {
		return nil, fmt.Errorf("%w: exactly one coordinator is required", ErrInvalidRollout)
	}
	// Stable sort preserves the owner's explicit order within each role.
	sort.SliceStable(copyNodes, func(i, j int) bool {
		return copyNodes[i].Role == RoleExecutor && copyNodes[j].Role == RoleCoordinator
	})
	return &Rollout{TargetVersion: targetVersion, RolloutState: RolloutRunning, OrderedNodes: copyNodes}, nil
}

func (rollout *Rollout) Status() RolloutStatus {
	if rollout == nil {
		return RolloutStopped
	}
	return rollout.RolloutState
}

func (rollout *Rollout) Order() []string {
	if rollout == nil {
		return nil
	}
	result := make([]string, len(rollout.OrderedNodes))
	for index := range rollout.OrderedNodes {
		result[index] = rollout.OrderedNodes[index].ID
	}
	return result
}

func (rollout *Rollout) Nodes() []Node {
	if rollout == nil {
		return nil
	}
	return append([]Node(nil), rollout.OrderedNodes...)
}

// NextAction issues at most one install or rollback action. Unknown and offline
// availability block the current node and stop the whole sequence.
func (rollout *Rollout) NextAction() (Action, error) {
	if rollout == nil || rollout.RolloutState != RolloutRunning || rollout.CurrentIndex < 0 || rollout.CurrentIndex >= len(rollout.OrderedNodes) {
		return Action{}, ErrUnexpectedState
	}
	node := &rollout.OrderedNodes[rollout.CurrentIndex]
	switch node.State {
	case NodePending:
		if err := rollout.requireOnline(node); err != nil {
			return Action{}, err
		}
		node.State = NodeInstalling
		return Action{Kind: ActionInstall, NodeID: node.ID, Version: rollout.TargetVersion}, nil
	case NodeRollbackPending:
		if err := rollout.requireOnline(node); err != nil {
			return Action{}, err
		}
		node.State = NodeRollingBack
		return Action{Kind: ActionRollback, NodeID: node.ID, Version: node.CurrentVersion}, nil
	default:
		return Action{}, ErrUnexpectedState
	}
}

// RecordApplied confirms only that the external installer finished. Readiness
// still requires a separate, exact-version HealthReceipt.
func (rollout *Rollout) RecordApplied(nodeID string) error {
	node, err := rollout.currentNode(nodeID)
	if err != nil {
		return err
	}
	switch node.State {
	case NodeInstalling:
		node.State = NodeAwaitingHealth
	case NodeRollingBack:
		node.State = NodeAwaitingRollbackHealth
	default:
		return ErrUnexpectedState
	}
	return nil
}

// RecordApplyFailure schedules rollback of the current node. Even an install
// failure is treated as potentially mutating and therefore requires rollback.
func (rollout *Rollout) RecordApplyFailure(nodeID string) error {
	node, err := rollout.currentNode(nodeID)
	if err != nil {
		return err
	}
	switch node.State {
	case NodeInstalling:
		node.State = NodeRollbackPending
		return nil
	case NodeRollingBack:
		node.State = NodeRollbackFailed
		rollout.RolloutState = RolloutRollbackFailed
		return ErrRollbackFailed
	default:
		return ErrUnexpectedState
	}
}

func (rollout *Rollout) RecordHealth(receipt HealthReceipt) error {
	node, err := rollout.currentNode(receipt.NodeID)
	if err != nil {
		return err
	}
	switch node.State {
	case NodeAwaitingHealth:
		if !receipt.healthyFor(*node, rollout.TargetVersion) {
			node.State = NodeRollbackPending
			return ErrUnhealthyReceipt
		}
		node.State = NodeUpdated
		node.CurrentVersion = rollout.TargetVersion
		rollout.advance()
		return nil
	case NodeAwaitingRollbackHealth:
		if !receipt.healthyFor(*node, node.CurrentVersion) {
			node.State = NodeRollbackFailed
			rollout.RolloutState = RolloutRollbackFailed
			return ErrUnhealthyReceipt
		}
		node.State = NodeRolledBack
		rollout.RolloutState = RolloutStopped
		return nil
	default:
		return ErrUnexpectedState
	}
}

// SetAvailability updates only an observed transport fact. It never resumes a
// stopped rollout implicitly.
func (rollout *Rollout) SetAvailability(nodeID string, availability Availability) error {
	if availability != AvailabilityUnknown && availability != AvailabilityOffline && availability != AvailabilityOnline {
		return ErrInvalidRollout
	}
	for index := range rollout.OrderedNodes {
		if rollout.OrderedNodes[index].ID == nodeID {
			rollout.OrderedNodes[index].Availability = availability
			return nil
		}
	}
	return ErrInvalidRollout
}

// Resume is explicit and is allowed only for the current node after an
// availability stop and a fresh online observation.
func (rollout *Rollout) Resume() error {
	if rollout == nil || rollout.RolloutState != RolloutStopped || rollout.CurrentIndex < 0 || rollout.CurrentIndex >= len(rollout.OrderedNodes) {
		return ErrUnexpectedState
	}
	node := &rollout.OrderedNodes[rollout.CurrentIndex]
	if node.State != NodeBlocked || node.Availability != AvailabilityOnline ||
		(node.BlockedFrom != NodePending && node.BlockedFrom != NodeRollbackPending) {
		return ErrUnexpectedState
	}
	node.State = node.BlockedFrom
	node.BlockedFrom = ""
	rollout.RolloutState = RolloutRunning
	return nil
}

func (rollout *Rollout) currentNode(nodeID string) (*Node, error) {
	if rollout == nil || rollout.RolloutState != RolloutRunning || rollout.CurrentIndex < 0 || rollout.CurrentIndex >= len(rollout.OrderedNodes) {
		return nil, ErrUnexpectedState
	}
	node := &rollout.OrderedNodes[rollout.CurrentIndex]
	if node.ID != nodeID {
		return nil, ErrUnexpectedState
	}
	return node, nil
}

func (rollout *Rollout) advance() {
	rollout.CurrentIndex++
	if rollout.CurrentIndex == len(rollout.OrderedNodes) {
		rollout.RolloutState = RolloutCompleted
	}
}

func (rollout *Rollout) requireOnline(node *Node) error {
	if node.Availability == AvailabilityOnline {
		return nil
	}
	node.BlockedFrom = node.State
	node.State = NodeBlocked
	rollout.RolloutState = RolloutStopped
	return fmt.Errorf("%w: %s is %s", ErrNodeUnavailable, node.ID, node.Availability)
}
