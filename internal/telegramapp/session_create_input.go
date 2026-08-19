package telegramapp

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
)

// prepareCreateFlowInput prevents ordinary messages from falling through to a
// previously active session while the user is creating another one. Once the
// flow reaches the resume/fresh choice, typing is equivalent to Start fresh.
func (h *Handler) prepareCreateFlowInput(
	ctx context.Context,
	actor application.Principal,
) (active bool, ready bool, err error) {
	flow, err := h.activeCreateFlow(actor.UserID)
	if errors.Is(err, domain.ErrNotFound) {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if h.starter == nil || flow.nodeID == "" || flow.backend == "" ||
		flow.workdir == "" || flow.resumeTotal == 0 {
		return true, false, nil
	}
	_, err = h.starter.Create(ctx, actor, application.CreateSessionRequest{
		NodeID: flow.nodeID, Backend: flow.backend, Workdir: flow.workdir,
	})
	if err != nil {
		return true, false, err
	}
	h.clearCreateFlow(actor.UserID)
	return true, true, nil
}
