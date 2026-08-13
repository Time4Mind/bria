package application

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (p *TelegramProjector) Status(actor Principal) (telegramui.Screen, error) {
	return p.StatusMode(actor, telegramui.StatusChoose)
}

func (p *TelegramProjector) StatusMode(
	actor Principal,
	mode telegramui.StatusMode,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	leaderID := domain.NodeID("")
	if p.leaders != nil {
		leaderID = domain.NodeID(p.leaders.LeaderID())
	}
	nodes := visibleNodes(state, actor, leaderID)
	items := make([]telegramui.StatusItem, 0, len(nodes))
	now := time.Now()
	for _, node := range nodes {
		action := telegramui.ActionSelectNode
		if mode == telegramui.StatusLeader {
			action = telegramui.ActionStatusLeaderNode
		} else if mode == telegramui.StatusSettings {
			action = telegramui.ActionStatusSettingsNode
		}
		token, tokenErr := p.tokens.Node(actor.UserID, action, node.ID)
		if tokenErr != nil {
			return telegramui.Screen{}, tokenErr
		}
		item := telegramui.StatusItem{
			Token: token, Name: node.Name, Status: projectionNodeStatus(node.Status),
			Leader: node.ID == leaderID, ObservedAt: node.LastSeenAt,
			Quotas:   nodeQuotas(state, node),
			Disabled: !node.Enabled(),
		}
		if item.Leader && state.TemporaryLeader.NodeID == node.ID &&
			state.TemporaryLeader.Until.After(now) {
			item.PinnedMinutes = max(1, int(state.TemporaryLeader.Until.Sub(now).Minutes()+0.999))
		}
		items = append(items, item)
	}
	return telegramui.RenderStatus(telegramui.StatusInput{
		Copy: actorCopy(state, actor), Mode: mode, Now: now, Items: items,
	}), nil
}

func (p *TelegramProjector) ConfirmLeader(
	actor Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	node, ok := state.Nodes[nodeID]
	if !ok || !state.CanAccessNode(actor.UserID, nodeID) || node.Status == domain.NodeOffline {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	token, err := p.tokens.Node(actor.UserID, telegramui.ActionConfirmLeader, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderLeaderConfirmation(actorCopy(state, actor), node.Name, token), nil
}

func (p *TelegramProjector) NodeSettings(
	actor Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	return p.nodeSettings(actor, nodeID, telegramui.ActionStatusMode, "settings")
}

func (p *TelegramProjector) NodeSettingsFromSessions(
	actor Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	back, err := p.tokens.Node(actor.UserID, telegramui.ActionSelectNode, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return p.nodeSettings(actor, nodeID, telegramui.ActionSelectNode, back)
}

func (p *TelegramProjector) NodeSettingsWithReturn(
	actor Principal,
	nodeID domain.NodeID,
	backAction telegramui.Action,
	backToken telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	return p.nodeSettings(actor, nodeID, backAction, backToken)
}

func (p *TelegramProjector) nodeSettings(
	actor Principal,
	nodeID domain.NodeID,
	backAction telegramui.Action,
	backToken telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	node, ok := state.Nodes[nodeID]
	if !ok || !state.CanAccessNode(actor.UserID, nodeID) {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	names := make([]string, 0, len(node.Backends))
	for _, backend := range node.Backends {
		names = append(names, backend.Name)
	}
	if len(names) == 0 {
		names = append(names, "—")
	}
	disable, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeDisable, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	enable, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeEnable, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	remove, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeDelete, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	rename, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeRename, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	speech, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeSpeechSetup, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	live := 0
	for _, session := range state.Sessions {
		if session.NodeID == nodeID && session.IsLive() {
			live++
		}
	}
	canDisable := state.CanDisableNode(nodeID) == nil
	aliases, err := p.providerAliases(state, actor.UserID, node)
	if err != nil {
		return telegramui.Screen{}, err
	}
	copy := actorCopy(state, actor)
	speechStatus := copy.Text(i18n.ValueOff)
	if state.Preferences[actor.UserID].EffectiveVoiceBackend() != domain.VoiceOff {
		speechStatus = copy.Text(i18n.VoiceSetupRequired)
	}
	return telegramui.RenderNodeMembership(telegramui.NodeMembershipInput{
		Copy: copy, Node: node, Backends: strings.Join(names, ", "),
		Status: projectionNodeStatus(node.Status), LiveSessions: live, CanDisable: canDisable,
		DisableToken: disable, EnableToken: enable, DeleteToken: remove, RenameToken: rename,
		ProviderAliases: aliases,
		BackendChoices:  p.backendChoices(state, actor.UserID, node),
		SpeechStatus:    speechStatus,
		SpeechToken:     speech,
		BackAction:      backAction,
		BackToken:       backToken,
	}), nil
}

func (p *TelegramProjector) backendChoices(
	state *domain.State, userID domain.UserID, node domain.Node,
) []telegramui.NodeBackendItem {
	connected := make(map[string]bool, len(node.Backends))
	for _, backend := range node.Backends {
		connected[strings.ToLower(backend.Name)] = true
	}
	items := make([]telegramui.NodeBackendItem, 0, len(node.InstalledBackends))
	for _, backend := range node.InstalledBackends {
		name := strings.ToLower(strings.TrimSpace(backend.Name))
		if name == "" {
			continue
		}
		action := telegramui.ActionBackendConnect
		if connected[name] {
			action = telegramui.ActionBackendDisconnect
		}
		token, err := p.tokens.Choice(
			userID, action, "node_backend", string(node.ID)+"\x00"+name,
		)
		if err != nil {
			continue
		}
		items = append(items, telegramui.NodeBackendItem{
			Name: name, Version: backend.Version, Connected: connected[name], Token: token,
		})
	}
	return items
}

func (p *TelegramProjector) providerAliases(
	state *domain.State,
	userID domain.UserID,
	node domain.Node,
) ([]telegramui.ProviderAliasItem, error) {
	items := make([]telegramui.ProviderAliasItem, 0, len(node.Backends))
	for _, backend := range node.Backends {
		if strings.TrimSpace(backend.Name) == "" {
			continue
		}
		token, err := p.tokens.Choice(
			userID, telegramui.ActionProviderAlias, "provider_alias",
			string(node.ID)+"\x00"+backend.Name,
		)
		if err != nil {
			return nil, err
		}
		authToken, err := p.tokens.Choice(
			userID, telegramui.ActionProviderAuth, "provider_auth",
			string(node.ID)+"\x00"+backend.Name,
		)
		if err != nil {
			return nil, err
		}
		items = append(items, telegramui.ProviderAliasItem{
			Backend: backend.Name, Alias: state.ProviderAccountAlias(node.ID, backend.Name),
			Token: token, AuthToken: authToken,
		})
	}
	return items, nil
}

func (p *TelegramProjector) EnrollmentDetail(
	actor Principal,
	requestID string,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	request, ok := state.EnrollmentRequests[requestID]
	if !ok || request.Status != domain.EnrollmentPending {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	approve, err := p.tokens.Choice(
		actor.UserID, telegramui.ActionEnrollmentApprove, "enrollment", requestID,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	reject, err := p.tokens.Choice(
		actor.UserID, telegramui.ActionEnrollmentReject, "enrollment", requestID,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderEnrollmentDetail(telegramui.EnrollmentDetailInput{
		Copy: actorCopy(state, actor), Request: request, Approve: approve, Reject: reject,
	}), nil
}

func nodeQuotas(state *domain.State, node domain.Node) []domain.QuotaSnapshot {
	result := make([]domain.QuotaSnapshot, 0)
	seen := make(map[string]bool)
	for _, snapshot := range state.Quotas {
		if snapshot.NodeID == node.ID {
			result = append(result, snapshot)
			seen[strings.ToLower(snapshot.Backend)] = true
		}
	}
	for _, backend := range node.Backends {
		if name := strings.TrimSpace(backend.Name); name != "" && !seen[strings.ToLower(name)] {
			result = append(result, domain.QuotaSnapshot{NodeID: node.ID, Backend: name})
		}
	}
	slices.SortFunc(result, func(a, b domain.QuotaSnapshot) int {
		return cmp.Compare(strings.ToLower(a.Backend), strings.ToLower(b.Backend))
	})
	return result
}
