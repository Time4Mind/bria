package telegramapp

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type clusterUpdater interface {
	Start(context.Context) (domain.ClusterUpdate, error)
}

func (h *Handler) SetClusterUpdater(service *clusterupdate.Coordinator) error {
	if service == nil {
		return domain.ErrInvalidState
	}
	h.clusterUpdater = service
	return nil
}

func isClusterUpdateAction(action telegramui.Action) bool {
	return action == telegramui.ActionClusterUpdate ||
		action == telegramui.ActionClusterUpdateYes ||
		action == telegramui.ActionClusterUpdateRefresh
}

func (h *Handler) handleClusterUpdate(
	ctx context.Context, actor application.Principal, action telegramui.Action,
) (telegramui.Screen, error) {
	switch action {
	case telegramui.ActionClusterUpdate:
		return h.openClusterUpdate(actor, true)
	case telegramui.ActionClusterUpdateYes:
		return h.startClusterUpdate(ctx, actor)
	default:
		return h.openClusterUpdate(actor, false)
	}
}

func (h *Handler) openClusterUpdate(
	actor application.Principal, confirm bool,
) (telegramui.Screen, error) {
	update, nodes, err := h.service.ClusterUpdate(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if update != nil && (update.Active() || !confirm) {
		return telegramui.RenderClusterUpdate(h.copy(actor), *update, nodes), nil
	}
	if h.clusterUpdater == nil {
		return telegramui.RenderClusterUpdateUnavailable(h.copy(actor)), nil
	}
	return telegramui.RenderClusterUpdateConfirmation(h.copy(actor)), nil
}

func (h *Handler) startClusterUpdate(
	ctx context.Context, actor application.Principal,
) (telegramui.Screen, error) {
	if h.clusterUpdater == nil {
		return telegramui.RenderClusterUpdateUnavailable(h.copy(actor)), nil
	}
	if _, err := h.clusterUpdater.Start(ctx); err != nil {
		detail := strings.Join(strings.Fields(err.Error()), " ")
		if len(detail) > 160 {
			detail = detail[:160]
		}
		return telegramui.RenderClusterUpdateError(h.copy(actor), detail), nil
	}
	return h.openClusterUpdate(actor, false)
}
