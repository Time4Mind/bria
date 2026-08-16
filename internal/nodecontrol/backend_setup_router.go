package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/backendsetup"
)

type BackendSetupRouter struct {
	localNodeID string
	local       backendsetup.Service
	remote      backendsetup.Service
}

func NewBackendSetupRouter(
	localNodeID string, local backendsetup.Service, remote backendsetup.Service,
) (*BackendSetupRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("backend setup router dependencies are required")
	}
	return &BackendSetupRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *BackendSetupRouter) Start(
	ctx context.Context, request backendsetup.Request,
) (backendsetup.Status, error) {
	return r.service(request.NodeID).Start(ctx, request)
}

func (r *BackendSetupRouter) Status(
	ctx context.Context, request backendsetup.Request,
) (backendsetup.Status, error) {
	return r.service(request.NodeID).Status(ctx, request)
}

func (r *BackendSetupRouter) service(nodeID string) backendsetup.Service {
	if nodeID == r.localNodeID {
		return r.local
	}
	return r.remote
}

var _ backendsetup.Service = (*BackendSetupRouter)(nil)
