package telegramview

import (
	"fmt"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const archivePageSize = 6

func (p *Projector) SelectedNodeArchives(
	actor Principal,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return p.nodeArchives(state, actor, state.Navigation.ActiveNodeByUser[actor.UserID], 1)
}

func (p *Projector) OpenArchives(actor Principal) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	preferences := state.Preferences[actor.UserID]
	if preferences.SessionView == domain.ViewAllHosts {
		return p.archiveList(state, actor, "", 1)
	}
	selected := state.Navigation.ActiveNodeByUser[actor.UserID]
	if selected != "" && state.CanAccessNode(actor.UserID, selected) {
		return p.nodeArchives(state, actor, selected, 1)
	}
	nodes := visibleNodes(state, actor)
	if len(nodes) == 1 {
		return p.nodeArchives(state, actor, nodes[0].ID, 1)
	}
	archived := archivedSessions(state, actor, "")
	items := make([]telegramui.ArchiveNodeItem, 0, len(nodes))
	for _, node := range nodes {
		count := 0
		for _, session := range archived {
			if session.NodeID == node.ID {
				count++
			}
		}
		token, tokenErr := p.tokens.Node(
			actor.UserID, telegramui.ActionSelectArchiveNode, node.ID,
		)
		if tokenErr != nil {
			return telegramui.Screen{}, tokenErr
		}
		items = append(items, telegramui.ArchiveNodeItem{
			Token: token, Name: node.Name, Status: projectionNodeStatus(node.Status), Archives: count,
		})
	}
	return telegramui.RenderArchiveNodes(actorCopy(state, actor), items), nil
}

func (p *Projector) OpenArchivesPage(
	actor Principal,
	page int,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if state.Preferences[actor.UserID].SessionView == domain.ViewAllHosts {
		return p.archiveList(state, actor, "", page)
	}
	nodeID := archiveNodeForView(state, actor)
	return p.nodeArchives(state, actor, nodeID, page)
}

// ArchiveListPage returns the current page containing ref. Recomputing it from
// replicated state keeps History -> Inspect -> Back stable even when newer
// archives arrived after the original list was rendered.
func (p *Projector) ArchiveListPage(
	actor Principal,
	ref domain.SessionRef,
) (int, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return 0, err
	}
	nodeID := domain.NodeID("")
	if state.Preferences[actor.UserID].SessionView != domain.ViewAllHosts {
		nodeID = archiveNodeForView(state, actor)
		if nodeID == "" || nodeID != ref.NodeID {
			return 0, domain.ErrNotFound
		}
	}
	for index, session := range archivedSessions(state, actor, nodeID) {
		if session.Ref() == ref {
			return index/archivePageSize + 1, nil
		}
	}
	return 0, domain.ErrNotFound
}

func (p *Projector) NodeArchives(
	actor Principal,
	nodeID domain.NodeID,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return p.nodeArchives(state, actor, nodeID, 1)
}

func (p *Projector) nodeArchives(
	state *domain.State,
	actor Principal,
	nodeID domain.NodeID,
	page int,
) (telegramui.Screen, error) {
	if !state.CanAccessNode(actor.UserID, nodeID) {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	_, ok := state.Nodes[nodeID]
	if !ok {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	return p.archiveList(state, actor, nodeID, page)
}

func (p *Projector) archiveList(
	state *domain.State,
	actor Principal,
	nodeID domain.NodeID,
	page int,
) (telegramui.Screen, error) {
	filtered := archivedSessions(state, actor, nodeID)
	total := len(filtered)
	pages := max(1, (total+archivePageSize-1)/archivePageSize)
	page = min(max(1, page), pages)
	start := (page - 1) * archivePageSize
	end := min(total, start+archivePageSize)
	items := make([]telegramui.ArchiveItem, 0, end-start)
	for index, session := range filtered[start:end] {
		token, err := p.archiveSelectionToken(actor, session.Ref(), page)
		if err != nil {
			return telegramui.Screen{}, err
		}
		item := telegramui.ArchiveItem{
			Token: token, Name: session.Name,
			Description: append([]string(nil), session.ArchiveDescription...),
			Index:       start + index + 1,
		}
		if nodeID == "" {
			item.NodeName = state.Nodes[session.NodeID].Name
		}
		items = append(items, item)
	}
	previous, next, err := p.archiveNavigationTokens(actor, page, pages)
	if err != nil {
		return telegramui.Screen{}, err
	}
	copy := actorCopy(state, actor)
	title := copy.Text(i18n.ArchiveAllTitle)
	if nodeID != "" {
		title = copy.Format(i18n.NodeArchiveTitle, state.Nodes[nodeID].Name)
	}
	return telegramui.RenderArchives(telegramui.ArchiveListInput{
		Copy: copy, Title: title, Items: items, Page: page, Pages: pages,
		PreviousToken: previous, NextToken: next,
	}), nil
}

func archivedSessions(
	state *domain.State,
	actor Principal,
	nodeID domain.NodeID,
) []domain.Session {
	sessions := state.VisibleSessions(actor.UserID, false)
	filtered := make([]domain.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.State == domain.SessionArchived &&
			(nodeID == "" || session.NodeID == nodeID) {
			filtered = append(filtered, session)
		}
	}
	return filtered
}

func archiveNodeForView(state *domain.State, actor Principal) domain.NodeID {
	selected := state.Navigation.ActiveNodeByUser[actor.UserID]
	if selected != "" && state.CanAccessNode(actor.UserID, selected) {
		return selected
	}
	nodes := visibleNodes(state, actor)
	if len(nodes) == 1 {
		return nodes[0].ID
	}
	return ""
}

func (p *Projector) archiveSelectionToken(
	actor Principal,
	ref domain.SessionRef,
	page int,
) (telegramui.OpaqueToken, error) {
	if tokens, ok := p.tokens.(archiveProjectionTokens); ok {
		return tokens.Archive(actor.UserID, telegramui.ActionSelectArchive, ref, page)
	}
	return p.tokens.Session(actor.UserID, telegramui.ActionSelectArchive, ref)
}

func (p *Projector) archiveNavigationTokens(
	actor Principal,
	page, pages int,
) (telegramui.OpaqueToken, telegramui.OpaqueToken, error) {
	var previous, next telegramui.OpaqueToken
	var err error
	if pages > 1 {
		previousPage := page - 1
		if previousPage < 1 {
			previousPage = pages
		}
		previous, err = p.tokens.Choice(actor.UserID, telegramui.ActionArchivePrevious,
			"archive-page", fmt.Sprint(previousPage))
	}
	if err == nil && pages > 1 {
		nextPage := page + 1
		if nextPage > pages {
			nextPage = 1
		}
		next, err = p.tokens.Choice(actor.UserID, telegramui.ActionArchiveNext,
			"archive-page", fmt.Sprint(nextPage))
	}
	return previous, next, err
}
