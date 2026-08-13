package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/clusterupdate"
)

type UpdateRouter struct {
	localNodeID string
	local       clusterupdate.Service
	remote      *UpdateClient
}

func NewUpdateRouter(
	localNodeID string, local clusterupdate.Service, remote *UpdateClient,
) (*UpdateRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("update router dependencies are required")
	}
	return &UpdateRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *UpdateRouter) Inspect(ctx context.Context) (clusterupdate.VerifiedManifest, error) {
	return r.local.Inspect(ctx)
}

func (r *UpdateRouter) Start(
	ctx context.Context, request clusterupdate.Request,
) (clusterupdate.Status, error) {
	if request.NodeID == r.localNodeID {
		return r.local.Start(ctx, request)
	}
	return r.remote.Start(ctx, request)
}

func (r *UpdateRouter) Status(
	ctx context.Context, request clusterupdate.Request,
) (clusterupdate.Status, error) {
	if request.NodeID == r.localNodeID {
		return r.local.Status(ctx, request)
	}
	return r.remote.Status(ctx, request)
}

var _ clusterupdate.Service = (*UpdateRouter)(nil)
