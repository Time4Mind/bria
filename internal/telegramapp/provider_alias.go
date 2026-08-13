package telegramapp

import (
	"context"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type providerAliasFlow struct {
	NodeID    domain.NodeID
	Backend   string
	ExpiresAt time.Time
}

func (h *Handler) beginProviderAlias(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	available, err := h.service.ProviderAliasCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	candidates := make([]string, 0, len(available))
	for _, candidate := range available {
		candidates = append(candidates, string(candidate.NodeID)+"\x00"+candidate.Backend)
	}
	choice, err := h.tokens.ResolveChoice(
		actor.UserID, telegramui.ActionProviderAlias, "provider_alias", token, candidates,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeID, backend, ok := strings.Cut(choice, "\x00")
	if !ok {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	h.membershipMu.Lock()
	delete(h.contractFlows, actor.UserID)
	delete(h.renameFlows, actor.UserID)
	h.providerAliasFlows[actor.UserID] = providerAliasFlow{
		NodeID: domain.NodeID(nodeID), Backend: backend, ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	h.membershipMu.Unlock()
	back := h.nodeSettingsReturn(actor)
	return telegramui.RenderProviderAliasPromptWithBack(
		h.copy(actor), backend, back.Action, back.Token,
	), nil
}

func (h *Handler) awaitingProviderAlias(actor application.Principal) bool {
	h.membershipMu.Lock()
	defer h.membershipMu.Unlock()
	flow, ok := h.providerAliasFlows[actor.UserID]
	if !ok || !time.Now().Before(flow.ExpiresAt) {
		delete(h.providerAliasFlows, actor.UserID)
		return false
	}
	return true
}

func (h *Handler) acceptProviderAlias(
	ctx context.Context,
	actor application.Principal,
	chatID int64,
	alias string,
) error {
	h.membershipMu.Lock()
	flow, ok := h.providerAliasFlows[actor.UserID]
	h.membershipMu.Unlock()
	if !ok {
		return nil
	}
	if alias == "-" {
		alias = ""
	}
	if err := h.service.SetProviderAccountAlias(ctx, actor, flow.NodeID, flow.Backend, alias); err != nil {
		return h.sendProjected(ctx, chatID, h.nodeSettingsResult(actor, i18n.ToastUnavailable, ""), nil)
	}
	h.membershipMu.Lock()
	delete(h.providerAliasFlows, actor.UserID)
	h.membershipMu.Unlock()
	return h.sendProjected(ctx, chatID, h.nodeSettingsResult(actor, i18n.ProviderAliasSaved, ""), nil)
}
