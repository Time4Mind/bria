package telegramapp

import (
	"context"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type clusterUpdater interface {
	Start(context.Context) (domain.ClusterUpdate, error)
	Retry(context.Context) (domain.ClusterUpdate, error)
	Progress(context.Context, string) map[domain.NodeID]clusterupdate.Status
}

func (h *Handler) SetClusterUpdater(service clusterUpdater) error {
	if service == nil {
		return domain.ErrInvalidState
	}
	h.clusterUpdater = service
	return nil
}

func isClusterUpdateAction(action telegramui.Action) bool {
	return action == telegramui.ActionClusterUpdate ||
		action == telegramui.ActionClusterUpdateYes ||
		action == telegramui.ActionClusterUpdateRetry ||
		action == telegramui.ActionClusterUpdateRefresh
}

func (h *Handler) handleClusterUpdate(
	ctx context.Context, actor application.Principal, action telegramui.Action,
) (telegramui.Screen, error) {
	switch action {
	case telegramui.ActionClusterUpdate:
		return h.openClusterUpdate(ctx, actor, true)
	case telegramui.ActionClusterUpdateYes:
		return h.startClusterUpdate(ctx, actor)
	case telegramui.ActionClusterUpdateRetry:
		return h.retryClusterUpdate(ctx, actor)
	default:
		return h.openClusterUpdate(ctx, actor, false)
	}
}

func (h *Handler) retryClusterUpdate(
	ctx context.Context, actor application.Principal,
) (telegramui.Screen, error) {
	if h.clusterUpdater == nil {
		return telegramui.RenderClusterUpdateUnavailable(h.copy(actor)), nil
	}
	if _, err := h.clusterUpdater.Retry(ctx); err != nil {
		detail := strings.Join(strings.Fields(err.Error()), " ")
		if len(detail) > 160 {
			detail = detail[:160]
		}
		return telegramui.RenderClusterUpdateError(h.copy(actor), detail), nil
	}
	return h.openClusterUpdate(ctx, actor, false)
}

func (h *Handler) openClusterUpdate(
	ctx context.Context, actor application.Principal, confirm bool,
) (telegramui.Screen, error) {
	update, nodes, err := h.service.ClusterUpdate(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if update != nil && (update.Active() || !confirm) {
		return h.renderClusterUpdate(ctx, actor, *update, nodes), nil
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
	return h.openClusterUpdate(ctx, actor, false)
}

func (h *Handler) renderClusterUpdate(
	ctx context.Context,
	actor application.Principal,
	update domain.ClusterUpdate,
	nodes map[domain.NodeID]domain.Node,
) telegramui.Screen {
	live := make(map[domain.NodeID]telegramui.NodeUpdateProgress)
	if h.clusterUpdater != nil {
		for nodeID, status := range h.clusterUpdater.Progress(ctx, update.ID) {
			live[nodeID] = telegramui.NodeUpdateProgress{
				Phase: string(status.Phase), Progress: status.Progress, Error: status.Error,
			}
		}
	}
	return telegramui.RenderClusterUpdateProgress(h.copy(actor), update, nodes, live, time.Now())
}
