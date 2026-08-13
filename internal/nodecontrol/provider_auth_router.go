package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/providerauth"
)

type ProviderAuthRouter struct {
	localNodeID string
	local       providerauth.Service
	remote      providerauth.Service
}

func NewProviderAuthRouter(
	localNodeID string,
	local providerauth.Service,
	remote providerauth.Service,
) (*ProviderAuthRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("provider authentication router dependencies are required")
	}
	return &ProviderAuthRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *ProviderAuthRouter) Start(
	ctx context.Context,
	request providerauth.StartRequest,
) (providerauth.Status, error) {
	return r.service(request.NodeID).Start(ctx, request)
}

func (r *ProviderAuthRouter) Submit(
	ctx context.Context,
	request providerauth.SubmitRequest,
) (providerauth.Status, error) {
	return r.service(request.NodeID).Submit(ctx, request)
}

func (r *ProviderAuthRouter) Status(
	ctx context.Context,
	request providerauth.FlowRequest,
) (providerauth.Status, error) {
	return r.service(request.NodeID).Status(ctx, request)
}

func (r *ProviderAuthRouter) Cancel(ctx context.Context, request providerauth.FlowRequest) error {
	return r.service(request.NodeID).Cancel(ctx, request)
}

func (r *ProviderAuthRouter) service(nodeID string) providerauth.Service {
	if nodeID == r.localNodeID {
		return r.local
	}
	return r.remote
}

var _ providerauth.Service = (*ProviderAuthRouter)(nil)
