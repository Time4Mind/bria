package telegramapp

import (
	"slices"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (p *TelegramProjector) backendChoices(
	state *domain.State, userID domain.UserID, node domain.Node,
) []telegramui.NodeBackendItem {
	connected := make(map[string]bool, len(node.Backends))
	for _, backend := range node.Backends {
		connected[strings.ToLower(backend.Name)] = true
	}
	installed := make(map[string]domain.BackendDescriptor, len(node.InstalledBackends))
	for _, backend := range node.InstalledBackends {
		name := strings.ToLower(strings.TrimSpace(backend.Name))
		if name != "" {
			installed[name] = backend
		}
	}
	names := []string{"claude", "codex"}
	for name := range installed {
		if name != "claude" && name != "codex" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	items := make([]telegramui.NodeBackendItem, 0, len(names))
	for _, name := range names {
		action := telegramui.ActionBackendConnect
		descriptor, isInstalled := installed[name]
		if !isInstalled {
			action = telegramui.ActionBackendInstall
		}
		if connected[name] {
			action = telegramui.ActionBackendDisconnect
		}
		token, err := p.tokens.Choice(
			userID, action, "node_backend", string(node.ID)+"\x00"+name,
		)
		if err != nil {
			continue
		}
		openToken, err := p.tokens.Choice(
			userID, telegramui.ActionNodeBackend, "node_backend_open",
			string(node.ID)+"\x00"+name,
		)
		if err != nil {
			continue
		}
		items = append(items, telegramui.NodeBackendItem{
			Name: name, Version: descriptor.Version, Installed: isInstalled,
			Connected: connected[name], Token: token, OpenToken: openToken,
		})
	}
	return items
}
