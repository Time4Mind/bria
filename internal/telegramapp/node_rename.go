package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
)

type nodeRenameFlow struct {
	NodeID    domain.NodeID
	ExpiresAt time.Time
}

func (h *Handler) beginNodeRename(actor application.Principal, nodeID domain.NodeID) {
	h.membershipMu.Lock()
	delete(h.contractFlows, actor.UserID)
	delete(h.providerAliasFlows, actor.UserID)
	h.renameFlows[actor.UserID] = nodeRenameFlow{NodeID: nodeID, ExpiresAt: time.Now().Add(10 * time.Minute)}
	h.membershipMu.Unlock()
}

func (h *Handler) awaitingNodeRename(actor application.Principal) bool {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	flow, ok := h.renameFlows[actor.UserID]
	if !ok || !time.Now().Before(flow.ExpiresAt) {
		delete(h.renameFlows, actor.UserID)
		return false
	}
	return true
}

func (h *Handler) acceptNodeRename(
	ctx context.Context,
	actor application.Principal,
	chatID int64,
	name string,
) error {
	h.membershipMu.Lock()
	flow, ok := h.renameFlows[actor.UserID]
	h.membershipMu.Unlock()
	if !ok || name == "" {
		return h.sendProjected(ctx, chatID, h.nodeSettingsResult(actor, i18n.ToastUnavailable, ""), nil)
	}
	if err := h.service.RenameNode(ctx, actor, flow.NodeID, name); err != nil {
		return h.sendProjected(ctx, chatID, h.nodeSettingsResult(actor, i18n.ToastUnavailable, ""), nil)
	}
	h.membershipMu.Lock()
	delete(h.renameFlows, actor.UserID)
	h.membershipMu.Unlock()
	screen, err := h.projectNodeSettings(actor, flow.NodeID)
	return h.sendProjected(ctx, chatID, screen, err)
}
