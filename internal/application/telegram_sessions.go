package application

import (
	"fmt"
	"strconv"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const (
	nodePageSize    = 8
	sessionPageSize = 12
)

// OpenSessions preserves the configurable navigation fork. Host-first uses an
// actor-filtered node selector unless exactly one enabled node is visible;
// all-hosts opens one live grid.
func (p *TelegramProjector) OpenSessions(
	actor Principal,
) (telegramui.Screen, error) {
	return p.OpenSessionsPageWithContext(actor, 1, nil)
}

func (p *TelegramProjector) OpenSessionsWithContext(
	actor Principal,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	return p.OpenSessionsPageWithContext(actor, 1, contextPercent)
}

// OpenNodeSelector always renders the host selector. It is used by the
// explicit "Servers" back action and therefore must not follow SessionView or
// collapse directly into the only visible host.
func (p *TelegramProjector) OpenNodeSelector(actor Principal) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return p.projectNodesPage(state, actor, 1, false, nil)
}

func (p *TelegramProjector) OpenSessionsPage(
	actor Principal,
	page int,
) (telegramui.Screen, error) {
	return p.OpenSessionsPageWithContext(actor, page, nil)
}

func (p *TelegramProjector) OpenSessionsPageWithContext(
	actor Principal,
	page int,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		preferences = domain.DefaultUserPreferences()
	}
	switch preferences.SessionView {
	case domain.ViewHostFirst:
		return p.projectNodes(state, actor, page, contextPercent)
	case domain.ViewAllHosts:
		return p.projectAllSessions(state, actor, page, contextPercent)
	default:
		return telegramui.Screen{}, fmt.Errorf(
			"%w: unsupported session view", domain.ErrInvalidState,
		)
	}
}

func (p *TelegramProjector) NodeSessions(
	actor Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	return p.NodeSessionsPageWithContext(actor, nodeID, 1, nil)
}

func (p *TelegramProjector) NodeSessionsWithContext(
	actor Principal,
	nodeID domain.NodeID,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	return p.NodeSessionsPageWithContext(actor, nodeID, 1, contextPercent)
}

func (p *TelegramProjector) NodeSessionsPage(
	actor Principal,
	nodeID domain.NodeID,
	page int,
) (telegramui.Screen, error) {
	return p.NodeSessionsPageWithContext(actor, nodeID, page, nil)
}

func (p *TelegramProjector) NodeSessionsPageWithContext(
	actor Principal,
	nodeID domain.NodeID,
	page int,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if !state.CanAccessNode(actor.UserID, nodeID) {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	node, ok := state.Nodes[nodeID]
	if !ok {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	nodeItem := telegramui.NodeItem{
		Name: node.Name, Status: projectionNodeStatus(node.Status), Selected: true,
	}
	settings, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeSettings, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeItem.SettingsToken = settings
	newSession, err := p.tokens.Node(actor.UserID, telegramui.ActionNewSession, nodeID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeItem.NewToken = newSession
	if !nodeAvailable(node.Status) {
		last, err := p.offlineLastCard(state, actor, nodeID)
		if err != nil {
			return telegramui.Screen{}, err
		}
		return telegramui.RenderUnavailableNode(actorCopy(state, actor), nodeItem, last), nil
	}
	items, err := p.sessionItems(state, actor, nodeID, false, contextPercent)
	if err != nil {
		return telegramui.Screen{}, err
	}
	nodeItem.LiveSessions = len(items)
	pageItems, page, pages := sessionPage(items, page)
	previous, next, err := p.sessionNavigationTokens(actor, nodeID, page, pages)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderNodeSessionsPage(
		actorCopy(state, actor), nodeItem, pageItems, page, pages, previous, next,
	), nil
}

func (p *TelegramProjector) offlineLastCard(
	state *domain.State,
	actor Principal,
	nodeID domain.NodeID,
) (*telegramui.SessionItem, error) {
	sessionID := state.Navigation.ActiveSessionByUserNode[actor.UserID][nodeID]
	if sessionID == "" {
		return nil, nil
	}
	ref := domain.SessionRef{NodeID: nodeID, SessionID: sessionID}
	if !state.CanViewSession(actor.UserID, ref) {
		return nil, nil
	}
	session := state.Sessions[ref.Key()]
	token, err := p.tokens.Session(
		actor.UserID, telegramui.ActionSelectSession, ref,
	)
	if err != nil {
		return nil, err
	}
	return &telegramui.SessionItem{Token: token, Name: session.Name}, nil
}

func (p *TelegramProjector) projectNodes(
	state *domain.State,
	actor Principal,
	page int,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	return p.projectNodesPage(state, actor, page, true, contextPercent)
}

func (p *TelegramProjector) projectNodesPage(
	state *domain.State,
	actor Principal,
	page int,
	collapseSingle bool,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	selected := state.Navigation.ActiveNodeByUser[actor.UserID]
	nodes := visibleNodes(state, actor)
	if collapseSingle && len(nodes) == 1 {
		return p.projectSingleNodeSessions(state, actor, nodes[0], contextPercent)
	}
	page, pages, start, end := pageBounds(len(nodes), page, nodePageSize)
	items := make([]telegramui.NodeItem, 0, end-start)
	for _, node := range nodes[start:end] {
		item := telegramui.NodeItem{
			Name: node.Name, Status: projectionNodeStatus(node.Status),
			Selected: node.ID == selected,
		}
		token, err := p.tokens.Node(actor.UserID, telegramui.ActionSelectNode, node.ID)
		if err != nil {
			return telegramui.Screen{}, err
		}
		item.Token = token
		if nodeAvailable(node.Status) {
			item.LiveSessions = visibleLiveCount(state, actor, node.ID)
		}
		items = append(items, item)
	}
	previous, next, err := p.nodeNavigationTokens(actor, page, pages)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderHostFirstNodesPage(
		actorCopy(state, actor), items, page, pages, previous, next,
	), nil
}

func (p *TelegramProjector) projectSingleNodeSessions(
	state *domain.State,
	actor Principal,
	node domain.Node,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	item := telegramui.NodeItem{
		Name: node.Name, Status: projectionNodeStatus(node.Status), Selected: true,
	}
	settings, err := p.tokens.Node(actor.UserID, telegramui.ActionNodeSettings, node.ID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	item.SettingsToken = settings
	newSession, err := p.tokens.Node(actor.UserID, telegramui.ActionNewSession, node.ID)
	if err != nil {
		return telegramui.Screen{}, err
	}
	item.NewToken = newSession
	if !nodeAvailable(node.Status) {
		last, err := p.offlineLastCard(state, actor, node.ID)
		if err != nil {
			return telegramui.Screen{}, err
		}
		return telegramui.RenderUnavailableNode(actorCopy(state, actor), item, last), nil
	}
	items, err := p.sessionItems(state, actor, node.ID, false, contextPercent)
	if err != nil {
		return telegramui.Screen{}, err
	}
	item.LiveSessions = len(items)
	pageItems, page, pages := sessionPage(items, 1)
	previous, next, err := p.sessionNavigationTokens(actor, node.ID, page, pages)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderNodeSessionsPage(
		actorCopy(state, actor), item, pageItems, page, pages, previous, next,
	), nil
}

func (p *TelegramProjector) projectAllSessions(
	state *domain.State,
	actor Principal,
	page int,
	contextPercent map[string]int,
) (telegramui.Screen, error) {
	items, err := p.sessionItems(state, actor, "", true, contextPercent)
	if err != nil {
		return telegramui.Screen{}, err
	}
	pageItems, page, pages := sessionPage(items, page)
	previous, next, err := p.sessionNavigationTokens(actor, "", page, pages)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderAllHostSessionsPage(
		actorCopy(state, actor), pageItems, page, pages, previous, next,
	), nil
}

func sessionPage(items []telegramui.SessionItem, page int) ([]telegramui.SessionItem, int, int) {
	page, pages, start, end := pageBounds(len(items), page, sessionPageSize)
	return items[start:end], page, pages
}

func pageBounds(total, page, size int) (int, int, int, int) {
	pages := max(1, (total+size-1)/size)
	page = min(max(1, page), pages)
	start := min(total, (page-1)*size)
	return page, pages, start, min(total, start+size)
}

func (p *TelegramProjector) nodeNavigationTokens(
	actor Principal,
	page, pages int,
) (telegramui.OpaqueToken, telegramui.OpaqueToken, error) {
	return p.listNavigationTokens(actor, "nodes-page", telegramui.ActionNodesPrevious,
		telegramui.ActionNodesNext, page, pages)
}

func (p *TelegramProjector) sessionNavigationTokens(
	actor Principal,
	nodeID domain.NodeID,
	page, pages int,
) (telegramui.OpaqueToken, telegramui.OpaqueToken, error) {
	return p.listNavigationTokens(actor, "sessions-page-"+string(nodeID),
		telegramui.ActionSessionsPrevious, telegramui.ActionSessionsNext, page, pages)
}

func (p *TelegramProjector) listNavigationTokens(
	actor Principal,
	flow string,
	previousAction, nextAction telegramui.Action,
	page, pages int,
) (telegramui.OpaqueToken, telegramui.OpaqueToken, error) {
	var previous, next telegramui.OpaqueToken
	var err error
	if pages > 1 {
		previousPage := page - 1
		if previousPage < 1 {
			previousPage = pages
		}
		previous, err = p.tokens.Choice(
			actor.UserID, previousAction, flow, strconv.Itoa(previousPage),
		)
	}
	if err == nil && pages > 1 {
		nextPage := page + 1
		if nextPage > pages {
			nextPage = 1
		}
		next, err = p.tokens.Choice(
			actor.UserID, nextAction, flow, strconv.Itoa(nextPage),
		)
	}
	return previous, next, err
}

func (p *TelegramProjector) sessionItems(
	state *domain.State,
	actor Principal,
	nodeFilter domain.NodeID,
	withNode bool,
	contextPercent map[string]int,
) ([]telegramui.SessionItem, error) {
	sessions := state.VisibleSessions(actor.UserID, true)
	items := make([]telegramui.SessionItem, 0, len(sessions))
	for _, session := range sessions {
		node, ok := state.Nodes[session.NodeID]
		if !ok || !nodeAvailable(node.Status) ||
			(nodeFilter != "" && session.NodeID != nodeFilter) {
			continue
		}
		token, err := p.tokens.Session(
			actor.UserID, telegramui.ActionSelectSession, session.Ref(),
		)
		if err != nil {
			return nil, err
		}
		item := telegramui.SessionItem{
			Token: token, Name: displaySessionName(session), Status: sessionStatusGlyph(session),
			NeedsInput: session.InteractivePrompt != nil,
		}
		if percent, present := contextPercent[session.Ref().Key()]; present {
			value := percent
			item.ContextPct = &value
		}
		if withNode {
			item.NodeName = node.Name
			item.Marker = nodeMarker(node.ID)
		}
		items = append(items, item)
	}
	return items, nil
}

func sessionStatusGlyph(session domain.Session) string {
	switch session.RuntimePhase {
	case domain.RuntimeStarting, domain.RuntimeRunning, domain.RuntimeStopping:
		return "⏳"
	case domain.RuntimeWaitingInput:
		return "❓"
	case domain.RuntimeDegraded:
		if session.LastOperation != nil &&
			session.LastOperation.Status == domain.OperationFailed {
			return "❌"
		}
		return "⚠️"
	case domain.RuntimeIdle:
		return "🟢"
	default:
		return "⚪"
	}
}

func visibleLiveCount(
	state *domain.State,
	actor Principal,
	nodeID domain.NodeID,
) int {
	count := 0
	for _, session := range state.VisibleSessions(actor.UserID, true) {
		if session.NodeID == nodeID {
			count++
		}
	}
	return count
}
