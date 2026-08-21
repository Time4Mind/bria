package sessioncontrol

import (
	"context"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/interaction"
	"github.com/Time4Mind/bria/internal/processlog"
)

func logInteractionOperation(
	ctx context.Context,
	ref domain.SessionRef,
	generation uint64,
	operationID string,
	action string,
) {
	ingress, ok := interaction.IngressFromContext(ctx)
	if !ok {
		return
	}
	processlog.Detailf(
		"bria interaction: stage=operation_link interaction=%q adapter=%s kind=%s operation=%q action=%s ref=%q generation=%d",
		ingress.ID(), ingress.Adapter(), ingress.Kind(), operationID, action, ref.Key(), generation,
	)
}
