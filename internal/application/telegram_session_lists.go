package application

import (
	"strconv"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

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
