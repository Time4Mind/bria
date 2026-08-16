package telegramapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/backendsetup"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) handleNodeBackendInstallCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	if err := h.messenger.AnswerCallbackQuery(ctx, update.CallbackID, ""); err != nil {
		return err
	}
	if h.backendSetup == nil {
		return domain.ErrInvalidState
	}
	nodeID, backend, nodeName, err := h.resolveNodeBackendInstall(actor, callback.Token)
	if err != nil {
		return nil
	}
	request := backendsetup.Request{NodeID: string(nodeID), Backend: backend}
	back, err := h.nodeBackendDetailToken(actor, nodeID, backend)
	if err != nil {
		return err
	}
	status, err := h.backendSetup.Start(ctx, request)
	if err != nil {
		text := h.copy(actor).Format(
			i18n.BackendInstallFailed, backend, nodeName, shortSetupError(err),
		)
		_, editErr := h.messenger.EditScreen(
			ctx, update.CallbackOrigin,
			telegramui.RenderBackendSetup(h.copy(actor), text, callback.Token, back),
		)
		return editErr
	}
	text := h.copy(actor).Format(
		i18n.BackendInstalling, backend, nodeName,
	)
	message, err := h.messenger.EditScreen(
		ctx, update.CallbackOrigin, telegramui.RenderBackendSetup(h.copy(actor), text, "", back),
	)
	if err != nil {
		return err
	}
	if status.Phase != backendsetup.PhaseReady {
		go h.watchBackendSetup(actor, request, nodeName, callback.Token, back, message)
		return nil
	}
	go h.watchBackendSetup(actor, request, nodeName, callback.Token, back, message)
	return nil
}

func (h *Handler) watchBackendSetup(
	actor application.Principal,
	request backendsetup.Request,
	nodeName string,
	retry telegramui.OpaqueToken,
	back telegramui.OpaqueToken,
	message telegrambot.Message,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		status, err := h.backendSetup.Status(ctx, request)
		if err != nil {
			return
		}
		switch status.Phase {
		case backendsetup.PhaseFailed:
			text := h.copy(actor).Format(
				i18n.BackendInstallFailed, request.Backend, nodeName, status.Detail,
			)
			_, _ = h.messenger.EditScreen(
				ctx, message, telegramui.RenderBackendSetup(h.copy(actor), text, retry, back),
			)
			return
		case backendsetup.PhaseReady:
			if h.connectInstalledBackend(ctx, actor, domain.NodeID(request.NodeID), request.Backend) {
				text := h.copy(actor).Format(
					i18n.BackendInstallReady, request.Backend, nodeName,
				)
				_, _ = h.messenger.EditScreen(
					ctx, message, telegramui.RenderBackendSetup(h.copy(actor), text, "", back),
				)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) connectInstalledBackend(
	ctx context.Context,
	actor application.Principal,
	nodeID domain.NodeID,
	backend string,
) bool {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return false
	}
	for _, item := range nodes {
		if item.Node.ID != nodeID {
			continue
		}
		for _, installed := range item.Node.InstalledBackends {
			if strings.EqualFold(installed.Name, backend) {
				scope := fmt.Sprintf("backend-setup-connect-%s-%s", nodeID, backend)
				err = h.service.SetNodeBackendConnected(
					application.WithOperationScope(ctx, scope), actor, nodeID, backend, true,
				)
				return err == nil
			}
		}
	}
	return false
}

func (h *Handler) resolveNodeBackendInstall(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (domain.NodeID, string, string, error) {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return "", "", "", err
	}
	values := make([]string, 0)
	names := make(map[string]string, len(nodes))
	for _, item := range nodes {
		installed := make(map[string]bool, len(item.Node.InstalledBackends))
		for _, descriptor := range item.Node.InstalledBackends {
			installed[strings.ToLower(descriptor.Name)] = true
		}
		for _, backend := range []string{"claude", "codex"} {
			if !installed[backend] {
				value := string(item.Node.ID) + "\x00" + backend
				values = append(values, value)
				names[value] = item.Node.Name
			}
		}
	}
	value, err := h.tokens.ResolveChoice(
		actor.UserID, telegramui.ActionBackendInstall, "node_backend", token, values,
	)
	if err != nil {
		return "", "", "", err
	}
	nodeID, backend, ok := strings.Cut(value, "\x00")
	if !ok {
		return "", "", "", domain.ErrNotFound
	}
	return domain.NodeID(nodeID), backend, names[value], nil
}

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
	screen, err := h.projector.NodeBackend(actor, nodeID, backend)
	if err == nil {
		_, err = h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
	}
	return err
}

func (h *Handler) openNodeBackends(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	nodeID, err := h.resolveStatusNode(actor, telegramui.ActionNodeBackends, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.NodeBackends(actor, nodeID)
}

func (h *Handler) openNodeBackend(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	values, err := h.nodeBackendMenuCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	value, err := h.tokens.ResolveChoice(
		actor.UserID, telegramui.ActionNodeBackend, "node_backend_open", token, values,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	node, backend, ok := strings.Cut(value, "\x00")
	if !ok {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	return h.projector.NodeBackend(actor, domain.NodeID(node), backend)
}

func (h *Handler) nodeBackendMenuCandidates(
	actor application.Principal,
) ([]string, error) {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, len(nodes)*2)
	for _, item := range nodes {
		names := map[string]struct{}{"claude": {}, "codex": {}}
		for _, backend := range item.Node.InstalledBackends {
			name := strings.ToLower(strings.TrimSpace(backend.Name))
			if name != "" {
				names[name] = struct{}{}
			}
		}
		for _, backend := range item.Node.Backends {
			name := strings.ToLower(strings.TrimSpace(backend.Name))
			if name != "" {
				names[name] = struct{}{}
			}
		}
		for name := range names {
			values = append(values, string(item.Node.ID)+"\x00"+name)
		}
	}
	return values, nil
}

func (h *Handler) nodeBackendDetailToken(
	actor application.Principal,
	nodeID domain.NodeID,
	backend string,
) (telegramui.OpaqueToken, error) {
	return h.tokens.Choice(
		actor.UserID, telegramui.ActionNodeBackend, "node_backend_open",
		string(nodeID)+"\x00"+strings.ToLower(strings.TrimSpace(backend)),
	)
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
