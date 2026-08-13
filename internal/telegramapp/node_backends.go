package telegramapp

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) handleNodeBackendCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		return err
	}
	nodeID, backend, err := h.resolveNodeBackend(actor, callback.Action, callback.Token)
	if err != nil {
		return nil
	}
	connected := callback.Action == telegramui.ActionBackendConnect
	if err := h.service.SetNodeBackendConnected(ctx, actor, nodeID, backend, connected); err != nil {
		return err
	}
	if connected {
		if flow, flowErr := h.activeCreateFlow(actor.UserID); flowErr == nil && flow.nodeID == nodeID {
			nodes, listErr := h.service.ListNodes(actor)
			if listErr != nil {
				return listErr
			}
			for _, item := range nodes {
				if item.Node.ID != nodeID {
					continue
				}
				screen, _, beginErr := h.beginCreateOnNode(ctx, actor, flow, item)
				if beginErr != nil {
					return beginErr
				}
				_, editErr := h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
				return editErr
			}
		}
	}
	screen, err := h.projectNodeSettings(actor, nodeID)
	if err == nil {
		_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
	}
	return err
}

func (h *Handler) resolveNodeBackend(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (domain.NodeID, string, error) {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return "", "", err
	}
	values := make([]string, 0)
	for _, item := range nodes {
		connected := make(map[string]bool, len(item.Node.Backends))
		for _, backend := range item.Node.Backends {
			connected[strings.ToLower(backend.Name)] = true
		}
		for _, backend := range item.Node.InstalledBackends {
			name := strings.ToLower(strings.TrimSpace(backend.Name))
			if name == "" || (action == telegramui.ActionBackendConnect) == connected[name] {
				continue
			}
			values = append(values, string(item.Node.ID)+"\x00"+name)
		}
	}
	value, err := h.tokens.ResolveChoice(actor.UserID, action, "node_backend", token, values)
	if err != nil {
		return "", "", err
	}
	node, backend, ok := strings.Cut(value, "\x00")
	if !ok {
		return "", "", domain.ErrNotFound
	}
	return domain.NodeID(node), backend, nil
}
