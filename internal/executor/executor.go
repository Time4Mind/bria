// Package executor defines the route-neutral coordinator-to-executor boundary.
// It does not know Codex, Claude, Telegram, or a concrete network transport.
package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bria/internal/computer"
	"bria/internal/domain"
)

var (
	ErrInvalidRequest  = errors.New("invalid executor request")
	ErrInvalidResponse = errors.New("invalid executor response")
	ErrComputerOffline = errors.New("executor computer is offline")
)

type Action string

const (
	ActionStart     Action = "start"
	ActionResume    Action = "resume"
	ActionSubmit    Action = "submit"
	ActionInterrupt Action = "interrupt"
	ActionStop      Action = "stop"
	ActionClose     Action = "close"
	ActionHistory   Action = "history"
	ActionScreen    Action = "screen"
)

func (action Action) valid() bool {
	switch action {
	case ActionStart, ActionResume, ActionSubmit, ActionInterrupt, ActionStop, ActionClose, ActionHistory, ActionScreen:
		return true
	default:
		return false
	}
}

type Request struct {
	OperationID string
	Generation  computer.CoordinatorGeneration
	SessionID   domain.SessionID
	Action      Action
	Payload     []byte
}

type Response struct {
	OperationID string
	Accepted    bool
	Payload     []byte
}

type Executor interface {
	Execute(ctx context.Context, request Request) (Response, error)
}

type Local struct{ handler Executor }

var _ Executor = (*Local)(nil)

func NewLocal(handler Executor) (*Local, error) {
	if handler == nil {
		return nil, ErrInvalidRequest
	}
	return &Local{handler: handler}, nil
}

func (local *Local) Execute(ctx context.Context, request Request) (Response, error) {
	if local == nil || local.handler == nil {
		return Response{}, ErrComputerOffline
	}
	if err := validateRequest(request); err != nil {
		return Response{}, err
	}
	response, err := local.handler.Execute(ctx, cloneRequest(request))
	if err != nil {
		return Response{}, err
	}
	return validateResponse(request, response)
}

// Transport is declared by its consumer. A concrete node-link adapter maps
// these values to authenticated versioned envelopes.
type Transport interface {
	Execute(ctx context.Context, target domain.ComputerID, request Request) (Response, error)
}

type Remote struct {
	target    domain.ComputerID
	transport Transport
}

var _ Executor = (*Remote)(nil)

func NewRemote(target domain.ComputerID, transport Transport) (*Remote, error) {
	if strings.TrimSpace(string(target)) == "" || transport == nil {
		return nil, ErrInvalidRequest
	}
	return &Remote{target: target, transport: transport}, nil
}

func (remote *Remote) Execute(ctx context.Context, request Request) (Response, error) {
	if remote == nil || remote.transport == nil {
		return Response{}, ErrComputerOffline
	}
	if err := validateRequest(request); err != nil {
		return Response{}, err
	}
	response, err := remote.transport.Execute(ctx, remote.target, cloneRequest(request))
	if err != nil {
		return Response{}, err
	}
	return validateResponse(request, response)
}

func validateRequest(request Request) error {
	if strings.TrimSpace(request.OperationID) == "" || request.Generation == 0 || strings.TrimSpace(string(request.SessionID)) == "" || !request.Action.valid() {
		return ErrInvalidRequest
	}
	return nil
}

func validateResponse(request Request, response Response) (Response, error) {
	if response.OperationID != request.OperationID {
		return Response{}, fmt.Errorf("%w: operation id mismatch", ErrInvalidResponse)
	}
	response.Payload = append([]byte(nil), response.Payload...)
	return response, nil
}

func cloneRequest(request Request) Request {
	request.Payload = append([]byte(nil), request.Payload...)
	return request
}
