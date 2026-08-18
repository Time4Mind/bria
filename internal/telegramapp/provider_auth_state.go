package telegramapp

import (
	"context"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

func (h *Handler) beginProviderAuthFlow(
	userID domain.UserID,
	flow providerAuthFlow,
) providerAuthFlow {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	h.providerAuthEpochs[userID]++
	flow.Epoch = h.providerAuthEpochs[userID]
	h.providerAuthFlows[userID] = flow
	return flow
}

func (h *Handler) replaceProviderAuthFlow(
	userID domain.UserID,
	flow providerAuthFlow,
) bool {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	current, ok := h.providerAuthFlows[userID]
	if !ok || current.Epoch != flow.Epoch {
		return false
	}
	h.providerAuthFlows[userID] = flow
	return true
}

func (h *Handler) takeProviderAuthFlow(
	userID domain.UserID,
	epoch uint64,
) (providerAuthFlow, bool) {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	flow, ok := h.providerAuthFlows[userID]
	if !ok || flow.Epoch != epoch {
		return providerAuthFlow{}, false
	}
	delete(h.providerAuthFlows, userID)
	return flow, true
}

func (h *Handler) finishProviderAuthFlow(
	ctx context.Context,
	actor application.Principal,
	flow providerAuthFlow,
	key i18n.Key,
	detail string,
) error {
	current, ok := h.takeProviderAuthFlow(actor.UserID, flow.Epoch)
	if !ok {
		return nil
	}
	return h.renderProviderAuthResult(ctx, actor, current.Carrier, current, key, detail)
}
