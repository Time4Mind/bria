package telegramview

import (
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (p *Projector) NodeBackends(
	actor Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	state, node, err := p.backendMenuNode(actor, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	back, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeSettings, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderNodeBackends(telegramui.NodeBackendsInput{
		Copy: actorCopy(state, actor), NodeName: node.Name,
		Items: p.backendChoices(state, actor.UserID, node), BackToken: back,
	}), nil
}

func (p *Projector) NodeBackend(
	actor Principal,
	nodeID domain.NodeID,
	backend string,
) (telegramui.Screen, error) {
	state, node, err := p.backendMenuNode(actor, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	backend = strings.ToLower(strings.TrimSpace(backend))
	var selected telegramui.NodeBackendItem
	for _, item := range p.backendChoices(state, actor.UserID, node) {
		if strings.EqualFold(item.Name, backend) {
			selected = item
			break
		}
	}
	if selected.Name == "" {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	back, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeBackends, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	input := telegramui.NodeBackendDetailInput{
		Copy: actorCopy(state, actor), NodeName: node.Name,
		Backend: selected, BackToken: back,
	}
	if selected.Connected {
		aliases, aliasesErr := p.providerAliases(state, actor.UserID, node)
		if aliasesErr != nil {
			return telegramui.Screen{}, aliasesErr
		}
		for _, item := range aliases {
			if strings.EqualFold(item.Backend, backend) {
				input.Alias, input.AliasToken, input.AuthToken = item.Alias, item.Token, item.AuthToken
				break
			}
		}
	}
	return telegramui.RenderNodeBackendDetail(input), nil
}

func (p *Projector) backendMenuNode(
	actor Principal,
	nodeID domain.NodeID,
) (*domain.State, domain.Node, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return nil, domain.Node{}, err
	}
	node, ok := state.Nodes[nodeID]
	if !ok || !state.CanAccessNode(actor.UserID, nodeID) {
		return nil, domain.Node{}, domain.ErrNotFound
	}
	return state, node, nil
}
