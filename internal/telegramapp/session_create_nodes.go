package telegramapp

import (
	"context"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) renderCreateNodes(actor application.Principal, flow *createFlow) (telegramui.Screen, domain.SessionRef, error) {
	items := make([]telegramui.CreateNodeItem, 0, len(flow.nodes))
	for _, item := range flow.nodes {
		token, err := h.tokens.Choice(actor.UserID, telegramui.ActionNewNode, flow.id, string(item.Node.ID))
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		items = append(items, telegramui.CreateNodeItem{
			Token: token, Name: item.Node.Name, Status: createNodeStatus(item.Node.Status),
		})
	}
	return telegramui.RenderCreateNodes(h.copy(actor), items), domain.SessionRef{}, nil
}

func createNodeStatus(status domain.NodeStatus) telegramui.NodeStatus {
	if status == domain.NodeOnline {
		return telegramui.NodeOnline
	}
	if status == domain.NodeReconnecting {
		return telegramui.NodeReconnecting
	}
	return telegramui.NodeOffline
}

func (h *Handler) chooseCreateNode(ctx context.Context, actor application.Principal, flow *createFlow, token telegramui.OpaqueToken) (telegramui.Screen, domain.SessionRef, error) {
	values := make([]string, len(flow.nodes))
	for index, item := range flow.nodes {
		values[index] = string(item.Node.ID)
	}
	value, err := h.tokens.ResolveChoice(actor.UserID, telegramui.ActionNewNode, flow.id, token, values)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	for _, item := range flow.nodes {
		if string(item.Node.ID) != value {
			continue
		}
		return h.beginCreateOnNode(ctx, actor, flow, item)
	}
	return telegramui.Screen{}, domain.SessionRef{}, domain.ErrNotFound
}

func (h *Handler) beginCreateOnNode(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
	item application.NodeItem,
) (telegramui.Screen, domain.SessionRef, error) {
	if !item.Node.Enabled() || item.Node.Status != domain.NodeOnline {
		return telegramui.Screen{}, domain.SessionRef{}, domain.ErrNotFound
	}
	flow.nodeID = item.Node.ID
	backends := createBackends(item.Node)
	if len(backends) == 0 {
		choices := make([]telegramui.CreateChoice, 0, len(item.Node.InstalledBackends))
		installed := item.Node.InstalledBackends
		if !item.Node.BackendExecutionAllowed() {
			installed = nil
		}
		for _, backend := range installed {
			name := strings.ToLower(strings.TrimSpace(backend.Name))
			if name == "" || !backendSupportsCreate(backend) {
				continue
			}
			token, tokenErr := h.tokens.Choice(
				actor.UserID, telegramui.ActionBackendConnect, "node_backend",
				string(item.Node.ID)+"\x00"+name,
			)
			if tokenErr != nil {
				return telegramui.Screen{}, domain.SessionRef{}, tokenErr
			}
			choices = append(choices, telegramui.CreateChoice{Token: token, Label: name})
		}
		return telegramui.RenderCreateNoBackends(
			h.copy(actor), item.Node.Name, choices,
		), domain.SessionRef{}, nil
	}
	if len(backends) == 1 {
		flow.backend = backends[0]
		return h.browseDirectory(ctx, actor, flow, "")
	}
	flow.backends = backends
	return h.renderCreateBackends(actor, flow, item.Node.Name, backends)
}

func (h *Handler) renderCreateBackends(actor application.Principal, flow *createFlow, nodeName string, backends []string) (telegramui.Screen, domain.SessionRef, error) {
	choices := make([]telegramui.CreateChoice, 0, len(backends))
	for _, backend := range backends {
		token, err := h.tokens.Choice(actor.UserID, telegramui.ActionNewBackend, flow.id, backend)
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		choices = append(choices, telegramui.CreateChoice{Token: token, Label: backend})
	}
	if flow.fromNode {
		return telegramui.RenderCreateBackendsWithBack(
			h.copy(actor), nodeName, choices, telegramui.ActionSessions, "",
		), domain.SessionRef{}, nil
	}
	return telegramui.RenderCreateBackends(h.copy(actor), nodeName, choices), domain.SessionRef{}, nil
}
