package telegramapp

import (
	"fmt"
	"strings"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

// CardPageLatestResponseStart selects the page containing the beginning of
// the most recent completed assistant response. It is used only for the final
// response carrier; zero continues to mean the live latest page.
const CardPageLatestResponseStart = -1

func (p *TelegramProjector) SessionCard(
	actor Principal,
	ref domain.SessionRef,
) (telegramui.Screen, error) {
	return p.SessionCardPage(actor, ref, nil, 0)
}

func (p *TelegramProjector) SessionCardPage(
	actor Principal,
	ref domain.SessionRef,
	events []application.CardEvent,
	requestedPage int,
) (telegramui.Screen, error) {
	return p.SessionCardPageWithContext(actor, ref, events, requestedPage, CardContext{})
}

type CardContext struct {
	ActivePercent     *int
	BackgroundPercent map[string]int
	HideBackground    bool
}

func (p *TelegramProjector) SessionCardPageWithContext(
	actor Principal,
	ref domain.SessionRef,
	events []application.CardEvent,
	requestedPage int,
	context CardContext,
) (telegramui.Screen, error) {
	return p.SessionCardViewWithContext(actor, ref, events, requestedPage, "", context)
}

func (p *TelegramProjector) SessionCardViewWithContext(
	actor Principal,
	ref domain.SessionRef,
	events []application.CardEvent,
	requestedPage int,
	requestedAnchor string,
	context CardContext,
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
	pages := application.RenderCardEventPages(preferences, events, application.CardRenderOptions{})
	page := requestedPage
	anchorResolved := false
	if requestedAnchor != "" {
		for _, candidate := range pages.Pages {
			for _, anchor := range strings.Split(candidate.AnchorIndex, "\x00") {
				if anchor == requestedAnchor {
					page = candidate.Number
					anchorResolved = true
					break
				}
			}
			if anchorResolved {
				break
			}
		}
		if !anchorResolved {
			// The pinned chunk aged out of the bounded transcript. The nearest
			// surviving content is the new oldest page; retaining the old numeric
			// index would silently jump the user forward to unrelated text.
			page = 1
		}
	}
	if page == CardPageLatestResponseStart && pages.LatestResponseStart.Number > 0 {
		page = pages.LatestResponseStart.Number
	}
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
	parts := []string{fmt.Sprintf(
		"%s · %s · %s · %s",
		displaySessionName(session),
		node.Name,
		session.Backend,
		lastAgentActivity(events),
	), "─────"}
	metadata := ""
	if node.Status != domain.NodeOnline {
		metadata += actorCopy(state, actor).Text(i18n.CardServerUnavailable)
		if queueCount > 0 {
			metadata += "\n" + actorCopy(state, actor).Format(
				i18n.CardOfflineInputQueued, queueCount, queueLimit,
			)
		}
	}
	if metadata != "" {
		parts = append(parts, metadata)
	}
	if session.RuntimeIssue == domain.RuntimeIssueProviderHookUnavailable {
		parts = append(parts, actorCopy(state, actor).Text(i18n.ProviderHookUnavailable))
	}
	richMarkdown := ""
	if page <= len(pages.Pages) {
		richMarkdown = pages.Pages[page-1].RichMarkdown
	}
	if richMarkdown != "" {
		parts = append(parts, richMarkdown)
	}
	if page == pages.Latest.Number && session.LastOperation != nil &&
		session.LastOperation.Action == domain.ActionSendInput &&
		session.LastOperation.Status == domain.OperationFailed {
		parts = append(parts, actorCopy(state, actor).Text(i18n.InputFailed))
	}
	paneAnchorOffset := len(strings.Join(parts, "\n\n"))
	if context.ActivePercent != nil {
		parts = append(parts, "\u00a0", fmt.Sprintf("context: %d%%", *context.ActivePercent))
	}
	allHosts := preferences.SessionView == domain.ViewAllHosts
	var switcher []telegramui.SessionItem
	if session.IsLive() {
		nodeFilter := session.NodeID
		if allHosts {
			nodeFilter = ""
		}
		switcher, err = p.sessionItems(
			state, actor, nodeFilter, allHosts, context.BackgroundPercent,
		)
		if err != nil {
			return telegramui.Screen{}, err
		}
		if !context.HideBackground &&
			state.Navigation.ActiveNodeByUser[actor.UserID] == session.NodeID &&
			state.Navigation.ActiveSessionByUserNode[actor.UserID][session.NodeID] == session.ID {
			if panel := backgroundPanel(
				state, actor, session.Ref(), allHosts, context.BackgroundPercent,
			); panel != "" {
				parts = append(parts, "\u00a0", panel)
			}
		}
	}
	text := strings.Join(parts, "\n\n")
	screen := telegramui.RenderSessionCard(telegramui.CardInput{
		Copy: actorCopy(state, actor), Text: text, Access: access,
		RichMarkdown: richMarkdown != "",
		Owner:        state.CanPerformSessionAction(actor.UserID, session.Ref(), domain.ActionRename),
		Starting:     session.RuntimePhase == domain.RuntimeStarting,
		CanRestore: session.State == domain.SessionArchived && session.ArchiveReady &&
			state.CanPerformSessionAction(actor.UserID, session.Ref(), domain.ActionRestore) &&
			node.Status == domain.NodeOnline &&
			session.Workdir != "",
		AcceptsQueuedInput: session.IsLive() && node.Enabled() &&
			node.Status != domain.NodeOnline &&
			state.CanPerformSessionAction(actor.UserID, session.Ref(), domain.ActionSendInput),
		Busy: session.RuntimePhase == domain.RuntimeRunning ||
			session.RuntimePhase == domain.RuntimeStopping ||
			session.RuntimePhase == domain.RuntimeDegraded,
		HidePagination: cardMode == domain.ResponseCardsKeepLatest,
		Page:           page, Pages: len(pages.Pages), Tokens: tokens,
		Sessions: switcher, AllHosts: allHosts,
	})
	screen.PaneAnchorOffset = paneAnchorOffset
	pageAnchor := pages.Pages[page-1].Anchor
	if anchorResolved {
		pageAnchor = requestedAnchor
	}
	screen.Checkpoint = &telegramui.SessionCheckpoint{
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		Revision: session.Revision, EventAt: session.LastEventAt,
		PageAnchor: pageAnchor,
	}
	return screen, nil
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
