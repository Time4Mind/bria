// Package providerauth runs an interactive provider login on the node whose
// credential store will be changed. Credentials and CLI output never enter
// replicated cluster state.
package providerauth

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	BackendClaude = "claude"
	BackendCodex  = "codex"

	StateWaitingInput State = "waiting_input"
	StateWaitingUser  State = "waiting_user"
	StateSucceeded    State = "succeeded"
	StateFailed       State = "failed"
	StateCancelled    State = "cancelled"
)

var (
	ErrUnsupportedBackend = errors.New("provider authentication is unsupported")
	ErrFlowNotFound       = errors.New("provider authentication flow not found")
	ErrFlowNotWaiting     = errors.New("provider authentication flow is not waiting for input")
)

type State string

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

type StartRequest struct {
	ActorID int64  `json:"actor_id"`
	NodeID  string `json:"node_id"`
	Backend string `json:"backend"`
}

type FlowRequest struct {
	ActorID int64  `json:"actor_id"`
	NodeID  string `json:"node_id"`
	FlowID  string `json:"flow_id"`
}

type SubmitRequest struct {
	ActorID int64  `json:"actor_id"`
	NodeID  string `json:"node_id"`
	FlowID  string `json:"flow_id"`
	Code    string `json:"code"`
}

type Status struct {
	FlowID    string    `json:"flow_id"`
	Backend   string    `json:"backend"`
	State     State     `json:"state"`
	URL       string    `json:"url,omitempty"`
	UserCode  string    `json:"user_code,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Service interface {
	Start(context.Context, StartRequest) (Status, error)
	Submit(context.Context, SubmitRequest) (Status, error)
	Status(context.Context, FlowRequest) (Status, error)
	Cancel(context.Context, FlowRequest) error
}

type Closer interface {
	Close() error
}

func normalizeStart(request StartRequest) (StartRequest, error) {
	request.NodeID = strings.TrimSpace(request.NodeID)
	request.Backend = strings.ToLower(strings.TrimSpace(request.Backend))
	if request.ActorID <= 0 || request.NodeID == "" {
		return StartRequest{}, errors.New("actor and node are required")
	}
	if request.Backend != BackendClaude && request.Backend != BackendCodex {
		return StartRequest{}, ErrUnsupportedBackend
	}
	return request, nil
}

func validateFlowRequest(actorID int64, nodeID, flowID string) error {
	if actorID <= 0 || strings.TrimSpace(nodeID) == "" || !validFlowID(flowID) {
		return errors.New("invalid provider authentication request")
	}
	return nil
}

func validFlowID(value string) bool {
	if len(value) < 20 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}
