package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/transcript"
)

type StartRouter struct {
	localNodeID string
	local       sessionstart.Service
	remote      sessionstart.Service
}

func NewStartRouter(localNodeID string, local, remote sessionstart.Service) (*StartRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("session start router dependencies are required")
	}
	return &StartRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *StartRouter) Browse(ctx context.Context, request sessionstart.BrowseRequest) (sessionstart.BrowseResult, error) {
	if string(request.NodeID) == r.localNodeID {
		return r.local.Browse(ctx, request)
	}
	return r.remote.Browse(ctx, request)
}

func (r *StartRouter) Discover(ctx context.Context, request sessionstart.DiscoverRequest) (transcript.Discovery, error) {
	if string(request.NodeID) == r.localNodeID {
		return r.local.Discover(ctx, request)
	}
	return r.remote.Discover(ctx, request)
}

func (r *StartRouter) Provision(ctx context.Context, request sessionstart.ProvisionRequest) error {
	if string(request.Session.NodeID) == r.localNodeID {
		return r.local.Provision(ctx, request)
	}
	return r.remote.Provision(ctx, request)
}
