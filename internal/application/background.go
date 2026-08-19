package application

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func (s *Service) StatusFingerprint(actor Principal) string {
	state := s.reader.State()
	if state == nil {
		return ""
	}
	if _, ok := state.Users[actor.UserID]; !ok {
		return ""
	}
	keys := make([]string, 0, len(state.Quotas))
	for _, snapshot := range state.Quotas {
		if state.CanAccessNode(actor.UserID, snapshot.NodeID) {
			keys = append(keys, snapshot.Key()+"@"+snapshot.CollectedAt.UTC().Format(time.RFC3339Nano))
		}
	}
	slices.Sort(keys)
	return strings.Join(keys, ";")
}

type BackgroundDelivery struct {
	UserID   domain.UserID
	Session  domain.Session
	Node     domain.Node
	Notice   domain.BackgroundNotice
	SendPush bool
}

type RunningSession struct {
	Actor   Principal
	Session domain.Session
}

func (s *Service) BackgroundPanelUsers() []domain.UserID {
	state := s.reader.State()
	if state == nil {
		return nil
	}
	result := make([]domain.UserID, 0, len(state.TelegramResponseCards))
	for userID := range state.TelegramResponseCards {
		if _, ok := state.Users[userID]; ok {
			result = append(result, userID)
		}
	}
	slices.Sort(result)
	return result
}

func (s *Service) BackgroundDeliveries() []BackgroundDelivery {
	state := s.reader.State()
	if state == nil {
		return nil
	}
	result := make([]BackgroundDelivery, 0)
	for userID, notices := range state.Navigation.BackgroundByUser {
		preferences, ok := state.Preferences[userID]
		if !ok {
			preferences = domain.DefaultUserPreferences()
		}
		active := activeRef(state, userID)
		for _, notice := range notices {
			if notice.Dismissed {
				continue
			}
			session, sessionOK := state.Sessions[notice.Session.Key()]
			node, nodeOK := state.Nodes[notice.Session.NodeID]
			if !sessionOK || !nodeOK || !session.IsLive() || node.Status != domain.NodeOnline ||
				notice.Session == active || !state.CanViewSession(userID, notice.Session) {
				continue
			}
			canControl := state.CanControlSession(userID, notice.Session)
			storedKind := notice.Kind
			if kind, current := domain.CurrentBackgroundKind(session); current {
				notice.Kind = kind
			}
			sendPush := !notice.Notified && preferences.SendsBackgroundNotification(notice.Kind)
			if notice.Kind != storedKind {
				// A stale delivery checkpoint must never manufacture a notification
				// for a transition that was not durably published.
				sendPush = false
			}
			if notice.Kind == domain.BackgroundNeedsAction && !canControl {
				sendPush = false
			}
			result = append(result, BackgroundDelivery{
				UserID: userID, Session: session, Node: node, Notice: notice,
				SendPush: sendPush,
			})
		}
	}
	// Snapshots written before idle sessions were seeded into BackgroundByUser
	// still need a complete CCBot-style panel after upgrade or leader restart.
	// Synthesize already-notified finished rows; no push is emitted for work the
	// user previously watched in the active card.
	for userID := range state.TelegramResponseCards {
		active := activeRef(state, userID)
		notices := state.Navigation.BackgroundByUser[userID]
		for _, session := range state.Sessions {
			node, nodeOK := state.Nodes[session.NodeID]
			_, known := notices[session.Ref().Key()]
			if known || !nodeOK || node.Status != domain.NodeOnline || !session.IsLive() ||
				session.RuntimePhase != domain.RuntimeIdle || session.Ref() == active ||
				!state.CanViewSession(userID, session.Ref()) {
				continue
			}
			result = append(result, BackgroundDelivery{
				UserID: userID, Session: session, Node: node,
				Notice: domain.BackgroundNotice{
					Session: session.Ref(), Kind: domain.BackgroundFinished,
					EventRevision: session.Revision, ChangedAt: session.LastEventAt, Notified: true,
				},
			})
		}
	}
	slices.SortFunc(result, func(a, b BackgroundDelivery) int {
		if order := cmp.Compare(a.UserID, b.UserID); order != 0 {
			return order
		}
		return cmp.Compare(a.Session.Ref().Key(), b.Session.Ref().Key())
	})
	return result
}

func (s *Service) RunningSessions() []RunningSession {
	state := s.reader.State()
	if state == nil {
		return nil
	}
	result := make([]RunningSession, 0)
	ownerID := state.OwnerID()
	for _, session := range state.Sessions {
		node, ok := state.Nodes[session.NodeID]
		if !ok || node.Status != domain.NodeOnline || !session.IsLive() ||
			session.RuntimePhase != domain.RuntimeRunning {
			continue
		}
		actorID := ownerID
		if actorID == 0 {
			actorID = session.OwnerID
		}
		result = append(result, RunningSession{Actor: Principal{UserID: actorID}, Session: session})
	}
	slices.SortFunc(result, func(a, b RunningSession) int {
		return cmp.Compare(a.Session.Ref().Key(), b.Session.Ref().Key())
	})
	if len(result) > 512 {
		result = result[:512]
	}
	return result
}

func activeRef(state *domain.State, userID domain.UserID) domain.SessionRef {
	nodeID := state.Navigation.ActiveNodeByUser[userID]
	return domain.SessionRef{
		NodeID: nodeID, SessionID: state.Navigation.ActiveSessionByUserNode[userID][nodeID],
	}
}
