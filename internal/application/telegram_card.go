package application

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (p *TelegramProjector) SessionCard(
	actor Principal,
	ref domain.SessionRef,
) (telegramui.Screen, error) {
	return p.SessionCardPage(actor, ref, nil, 0)
}

func (p *TelegramProjector) SessionCardPage(
	actor Principal,
	ref domain.SessionRef,
	events []CardEvent,
	requestedPage int,
) (telegramui.Screen, error) {
	state, err := p.actorState(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if !state.CanViewSession(actor.UserID, ref) {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	session, ok := state.Sessions[ref.Key()]
	if !ok {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	node, ok := state.Nodes[session.NodeID]
	if !ok || !state.CanAccessNode(actor.UserID, session.NodeID) {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	access := telegramui.SharedView
	if node.Status == domain.NodeOnline && state.CanControlSession(actor.UserID, ref) && session.IsLive() {
		access = telegramui.SharedControl
	}
	preferences := state.Preferences[actor.UserID]
	queueCount := len(state.DeferredInputs[ref.Key()])
	queueLimit := preferences.EffectiveOfflineInputQueueLimit()
	pages := RenderCardEventPages(preferences, events, CardRenderOptions{})
	page := requestedPage
	cardMode := preferences.EffectiveResponseCards()
	if cardMode == domain.ResponseCardsKeepLatest {
		page = pages.Latest.Number
	}
	if page <= 0 || page > len(pages.Pages) {
		page = pages.Latest.Number
	}
	if page < 1 {
		page = 1
	}
	tokens := make(map[telegramui.Action]telegramui.OpaqueToken)
	cardActions := []telegramui.Action{
		telegramui.ActionStop,
		telegramui.ActionClose,
		telegramui.ActionClear,
		telegramui.ActionTerminal,
	}
	if session.State == domain.SessionArchived {
		cardActions = append(cardActions, telegramui.ActionRestore)
	}
	for _, action := range cardActions {
		token, tokenErr := p.tokens.Session(actor.UserID, action, ref)
		if tokenErr != nil {
			return telegramui.Screen{}, tokenErr
		}
		tokens[action] = token
	}
	for _, target := range []struct {
		action telegramui.Action
		page   int
	}{
		{action: telegramui.ActionPagePrevious, page: wrappedPage(page-1, len(pages.Pages))},
		{action: telegramui.ActionPageLatest, page: len(pages.Pages)},
		{action: telegramui.ActionPageNext, page: wrappedPage(page+1, len(pages.Pages))},
	} {
		var token telegramui.OpaqueToken
		var tokenErr error
		if pageTokens, ok := p.tokens.(pageProjectionTokens); ok {
			token, tokenErr = pageTokens.Page(actor.UserID, target.action, ref, target.page)
		} else {
			token, tokenErr = p.tokens.Session(actor.UserID, target.action, ref)
		}
		if tokenErr != nil {
			return telegramui.Screen{}, tokenErr
		}
		tokens[target.action] = token
	}
	text := fmt.Sprintf(
		"%s · %s · %s · %s",
		displaySessionName(session),
		node.Name,
		session.Backend,
		lastAgentActivity(events),
	)
	if node.Status != domain.NodeOnline {
		text += "\n" + actorCopy(state, actor).Text(i18n.CardServerUnavailable)
		if queueCount > 0 {
			text += "\n" + actorCopy(state, actor).Format(
				i18n.CardOfflineInputQueued, queueCount, queueLimit,
			)
		}
	}
	if session.LastOperation != nil && session.LastOperation.Action == domain.ActionSendInput &&
		session.LastOperation.Status == domain.OperationFailed {
		text += "\n" + actorCopy(state, actor).Text(i18n.InputFailed)
	}
	richMarkdown := ""
	if page <= len(pages.Pages) {
		richMarkdown = pages.Pages[page-1].RichMarkdown
	}
	if richMarkdown != "" {
		text += "\n\n" + richMarkdown
	}
	allHosts := preferences.SessionView == domain.ViewAllHosts
	var switcher []telegramui.SessionItem
	if session.IsLive() {
		nodeFilter := session.NodeID
		if allHosts {
			nodeFilter = ""
		}
		switcher, err = p.sessionItems(state, actor, nodeFilter, allHosts)
		if err != nil {
			return telegramui.Screen{}, err
		}
		if state.Navigation.ActiveNodeByUser[actor.UserID] == session.NodeID &&
			state.Navigation.ActiveSessionByUserNode[actor.UserID][session.NodeID] == session.ID {
			if panel := backgroundPanel(state, actor, session.Ref(), allHosts); panel != "" {
				text += "\n\n" + panel
			}
		}
	}
	return telegramui.RenderSessionCard(telegramui.CardInput{
		Copy: actorCopy(state, actor), Text: text, Access: access,
		RichMarkdown: richMarkdown != "",
		Owner:        state.CanPerformSessionAction(actor.UserID, session.Ref(), domain.ActionRename),
		Starting:     session.RuntimePhase == domain.RuntimeStarting,
		CanRestore: session.State == domain.SessionArchived && session.ArchiveReady &&
			state.CanPerformSessionAction(actor.UserID, session.Ref(), domain.ActionRestore) &&
			node.Status == domain.NodeOnline &&
			session.ProviderSessionID != "" && session.Workdir != "",
		AcceptsQueuedInput: session.IsLive() && node.Enabled() &&
			node.Status != domain.NodeOnline &&
			state.CanPerformSessionAction(actor.UserID, session.Ref(), domain.ActionSendInput),
		Busy: session.RuntimePhase == domain.RuntimeRunning ||
			session.RuntimePhase == domain.RuntimeStopping ||
			session.RuntimePhase == domain.RuntimeDegraded,
		HidePagination: cardMode == domain.ResponseCardsKeepLatest,
		Page:           page, Pages: len(pages.Pages), Tokens: tokens,
		Sessions: switcher, AllHosts: allHosts,
	}), nil
}

func wrappedPage(page, pages int) int {
	if pages <= 1 {
		return 1
	}
	if page < 1 {
		return pages
	}
	if page > pages {
		return 1
	}
	return page
}

func backgroundPanel(
	state *domain.State,
	actor Principal,
	active domain.SessionRef,
	allHosts bool,
) string {
	const limit = 5
	notices := state.Navigation.BackgroundByUser[actor.UserID]
	items := make([]domain.BackgroundNotice, 0, len(notices))
	for _, notice := range notices {
		if notice.Dismissed {
			continue
		}
		session, ok := state.Sessions[notice.Session.Key()]
		node, nodeOK := state.Nodes[notice.Session.NodeID]
		if !ok || !nodeOK || !session.IsLive() || node.Status != domain.NodeOnline ||
			notice.Session == active || !state.CanViewSession(actor.UserID, notice.Session) ||
			(!allHosts && notice.Session.NodeID != active.NodeID) {
			continue
		}
		items = append(items, notice)
	}
	slices.SortFunc(items, func(a, b domain.BackgroundNotice) int {
		if order := b.ChangedAt.Compare(a.ChangedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.Session.Key(), b.Session.Key())
	})
	extra := max(0, len(items)-limit)
	items = items[:min(len(items), limit)]
	rendered := make([]telegramui.BackgroundItem, 0, len(items))
	for _, notice := range items {
		session := state.Sessions[notice.Session.Key()]
		name := session.Name
		if name == "" {
			name = "…"
		}
		item := telegramui.BackgroundItem{
			Name: name, Status: telegramui.BackgroundStatusGlyph(string(notice.Kind)),
		}
		if allHosts {
			item.NodeName = state.Nodes[session.NodeID].Name
			item.Marker = nodeMarker(session.NodeID)
		}
		rendered = append(rendered, item)
	}
	return telegramui.RenderBackgroundPanel(actorCopy(state, actor), rendered, extra)
}

func displaySessionName(session domain.Session) string {
	if session.Name != "" {
		return session.Name
	}
	return "…"
}

func lastAgentActivity(events []CardEvent) string {
	var latest time.Time
	for _, event := range events {
		if event.Kind == CardEventUserText || event.StartedAt.IsZero() {
			continue
		}
		if event.StartedAt.After(latest) {
			latest = event.StartedAt
		}
	}
	if latest.IsZero() {
		return "—"
	}
	return latest.Format("15:04:05")
}
