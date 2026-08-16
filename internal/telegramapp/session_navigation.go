package telegramapp

import (
	"context"
	"errors"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) openSessions(
	ctx context.Context,
	actor application.Principal,
) (telegramui.Screen, error) {
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if preferences.SessionView == domain.ViewAllHosts {
		if session, activeErr := h.service.ActiveSession(actor); activeErr == nil {
			return h.renderSessionCard(ctx, actor, session.Ref(), 0)
		} else if !errors.Is(activeErr, domain.ErrNotFound) {
			return telegramui.Screen{}, activeErr
		}
		return h.projector.OpenSessionsWithContext(actor, h.cachedContextPercents())
	}
	selected, found, err := h.service.SelectedNode(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if !found {
		return h.projector.OpenSessionsWithContext(actor, h.cachedContextPercents())
	}
	if nodeAvailableForCard(selected.Node) {
		session, activeErr := h.service.ActiveSession(actor)
		if activeErr == nil && session.NodeID == selected.Node.ID {
			return h.renderSessionCard(ctx, actor, session.Ref(), 0)
		}
		if activeErr != nil && !errors.Is(activeErr, domain.ErrNotFound) {
			return telegramui.Screen{}, activeErr
		}
	}
	return h.projector.NodeSessionsWithContext(
		actor, selected.Node.ID, h.cachedContextPercents(),
	)
}

func nodeAvailableForCard(node domain.Node) bool {
	return node.Status == domain.NodeOnline || node.Status == domain.NodeReconnecting
}

func (h *Handler) selectNode(
	ctx context.Context,
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	candidates, err := h.service.CallbackNodeCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeID, err := h.tokens.ResolveNode(actor.UserID, telegramui.ActionSelectNode, token, candidates)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.SelectNode(ctx, actor, nodeID); err != nil {
		return telegramui.Screen{}, err
	}
	selected, selectedOK, err := h.service.SelectedNode(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if selectedOK && selected.Node.Enabled() && nodeAvailableForCard(selected.Node) {
		session, activeErr := h.service.ActiveSession(actor)
		if activeErr == nil && session.NodeID == nodeID {
			return h.renderSessionCard(ctx, actor, session.Ref(), 0)
		}
		if activeErr != nil && !errors.Is(activeErr, domain.ErrNotFound) {
			return telegramui.Screen{}, activeErr
		}
	}
	return h.projector.NodeSessionsWithContext(actor, nodeID, h.cachedContextPercents())
}

func (h *Handler) selectSession(
	ctx context.Context,
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	ref, err := h.resolveSession(actor, action, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if err := h.service.SelectSession(ctx, actor, ref); err != nil {
		return telegramui.Screen{}, err
	}
	return h.renderSessionCard(ctx, actor, ref, 0)
}

func (h *Handler) resolveSession(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (domain.SessionRef, error) {
	candidates, err := h.service.CallbackSessionCandidates(actor)
	if err != nil {
		return domain.SessionRef{}, err
	}
	return h.tokens.ResolveSession(actor.UserID, action, token, candidates)
}
