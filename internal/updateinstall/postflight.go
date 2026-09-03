package updateinstall

import (
	"context"
	"errors"

	"bria/internal/update"
	"bria/internal/updateflow"
)

var (
	ErrInvalidPostflight  = errors.New("invalid packaged update postflight")
	ErrPostflightMismatch = errors.New("packaged update postflight mismatch")
)

type HealthReader interface {
	ReadHealth(context.Context, string, string) (update.HealthReceipt, error)
}

type CurrentIntegrityVerifier interface {
	VerifyCurrent(context.Context, string, string) error
}

// VersionedPostflight combines two independent rereads: externally observable
// service health and the version/state persisted by the packaged installation.
// It does not manufacture readiness flags when either reread is incomplete.
type VersionedPostflight struct {
	Health    HealthReader
	State     InstallStateReader
	Integrity CurrentIntegrityVerifier
}

func (p VersionedPostflight) Probe(ctx context.Context, nodeID, version string) (updateflow.PostflightReceipt, error) {
	if p.Health == nil || p.State == nil || p.Integrity == nil || invalidText(nodeID, 1024) || invalidText(version, 128) {
		return updateflow.PostflightReceipt{}, ErrInvalidPostflight
	}
	if err := p.Integrity.VerifyCurrent(ctx, nodeID, version); err != nil {
		return updateflow.PostflightReceipt{}, ErrPostflightMismatch
	}
	health, err := p.Health.ReadHealth(ctx, nodeID, version)
	if err != nil {
		return updateflow.PostflightReceipt{}, err
	}
	state, err := p.State.ReadInstalledState(ctx, nodeID)
	if err != nil {
		return updateflow.PostflightReceipt{}, err
	}
	if health.NodeID != nodeID || health.RunningVersion != version || state.Version != version ||
		invalidText(state.StateFingerprint, 4096) {
		return updateflow.PostflightReceipt{}, ErrPostflightMismatch
	}
	return updateflow.PostflightReceipt{Health: health, StateFingerprint: state.StateFingerprint}, nil
}
