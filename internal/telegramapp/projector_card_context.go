package telegramapp

import (
	"cmp"
	"slices"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func backgroundPanel(
	state *domain.State,
	actor Principal,
	active domain.SessionRef,
	allHosts bool,
	contextPercent map[string]int,
) string {
	const limit = 5
	notices := state.Navigation.BackgroundByUser[actor.UserID]
	items := make([]domain.BackgroundNotice, 0, len(notices))
	known := make(map[string]bool, len(notices))
	for _, notice := range notices {
		known[notice.Session.Key()] = true
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
	for _, session := range state.Sessions {
		node, nodeOK := state.Nodes[session.NodeID]
		if known[session.Ref().Key()] || !nodeOK || node.Status != domain.NodeOnline ||
			!session.IsLive() || session.RuntimePhase != domain.RuntimeIdle ||
			session.Ref() == active || !state.CanViewSession(actor.UserID, session.Ref()) ||
			(!allHosts && session.NodeID != active.NodeID) {
			continue
		}
		items = append(items, domain.BackgroundNotice{
			Session: session.Ref(), Kind: domain.BackgroundFinished,
			EventRevision: session.Revision, ChangedAt: session.LastEventAt, Notified: true,
		})
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
		if kind, ok := domain.CurrentBackgroundKind(session); ok {
			notice.Kind = kind
		}
		name := session.Name
		if name == "" {
			name = "…"
		}
		item := telegramui.BackgroundItem{
			Name: name, Status: telegramui.BackgroundStatusGlyph(string(notice.Kind)),
		}
		if percent, ok := contextPercent[notice.Session.Key()]; ok {
			item.ContextPercent = &percent
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

func lastAgentActivity(events []application.CardEvent) string {
	var latest time.Time
	for _, event := range events {
		if event.Kind == application.CardEventUserText || event.StartedAt.IsZero() {
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
