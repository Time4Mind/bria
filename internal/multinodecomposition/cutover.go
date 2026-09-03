package multinodecomposition

import (
	"context"

	"bria/internal/coordinatortransfer"
)

// ManualCutover is the only role-switching composition. Every phase requires
// the live old coordinator object; there is deliberately no election path.
func ManualCutover(ctx context.Context, source *coordinatortransfer.Source, target *coordinatortransfer.Target, request coordinatortransfer.Request, readiness coordinatortransfer.LiveReceipt) (coordinatortransfer.Activation, error) {
	if ctx == nil || source == nil || target == nil {
		return coordinatortransfer.Activation{}, ErrInvalidComposition
	}
	offer, err := source.Prepare(ctx, request, readiness)
	if err != nil {
		return coordinatortransfer.Activation{}, err
	}
	receipt, err := target.Stage(ctx, offer)
	if err != nil {
		return coordinatortransfer.Activation{}, err
	}
	commit, err := source.Commit(ctx, receipt)
	if err != nil {
		return coordinatortransfer.Activation{}, err
	}
	activation, err := target.Activate(ctx, commit)
	if err != nil {
		return coordinatortransfer.Activation{}, err
	}
	if err := source.Finalize(ctx, activation); err != nil {
		return coordinatortransfer.Activation{}, err
	}
	if source.CanCoordinate() || !target.CanCoordinate() {
		return coordinatortransfer.Activation{}, ErrInvalidComposition
	}
	return activation, nil
}
