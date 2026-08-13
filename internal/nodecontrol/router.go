package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/runtimehost"
)

type Router struct {
	localNodeID string
	local       RuntimeClient
	remote      RuntimeClient
}

func NewRouter(localNodeID string, local, remote RuntimeClient) (*Router, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("local node id and runtime submitters are required")
	}
	return &Router{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *Router) LookupResult(
	ctx context.Context,
	request runtimehost.Request,
) (runtimehost.Result, bool, error) {
	if request.NodeID == r.localNodeID {
		return r.local.LookupResult(ctx, request)
	}
	return r.remote.LookupResult(ctx, request)
}

func (r *Router) Submit(
	ctx context.Context,
	request runtimehost.Request,
) (runtimehost.Receipt, error) {
	if request.NodeID == r.localNodeID {
		return r.local.Submit(ctx, request)
	}
	return r.remote.Submit(ctx, request)
}
