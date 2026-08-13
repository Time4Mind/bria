package nodecontrol

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/speechsetup"
)

type SpeechSetupRouter struct {
	localNodeID string
	local       speechsetup.Service
	remote      speechsetup.Service
}

func NewSpeechSetupRouter(
	localNodeID string, local speechsetup.Service, remote speechsetup.Service,
) (*SpeechSetupRouter, error) {
	if localNodeID == "" || local == nil || remote == nil {
		return nil, errors.New("speech setup router dependencies are required")
	}
	return &SpeechSetupRouter{localNodeID: localNodeID, local: local, remote: remote}, nil
}

func (r *SpeechSetupRouter) Start(
	ctx context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	return r.service(request.NodeID).Start(ctx, request)
}

func (r *SpeechSetupRouter) Status(
	ctx context.Context, request speechsetup.Request,
) (speechsetup.Status, error) {
	return r.service(request.NodeID).Status(ctx, request)
}

func (r *SpeechSetupRouter) service(nodeID string) speechsetup.Service {
	if nodeID == r.localNodeID {
		return r.local
	}
	return r.remote
}

var _ speechsetup.Service = (*SpeechSetupRouter)(nil)
