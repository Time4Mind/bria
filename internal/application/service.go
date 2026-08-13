// Package application exposes actor-first Bria use cases to UI adapters.
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type Principal struct {
	UserID domain.UserID
}

type StateReader interface {
	State() *domain.State
}

type CommandApplier interface {
	Apply(context.Context, clusterstate.Command) (clusterstate.Result, error)
}

type Service struct {
	reader     StateReader
	applier    CommandApplier
	leaders    interface{ LeaderID() string }
	enrollment EnrollmentInvitationConfig
	now        func() time.Time
	newID      func() (string, error)
}

type NodeItem struct {
	Node         domain.Node
	LiveSessions int
}

type SessionItem struct {
	Session domain.Session
	Node    domain.Node
	Access  domain.ShareMode
}

func NewService(reader StateReader, applier CommandApplier) (*Service, error) {
	if reader == nil || applier == nil {
		return nil, errors.New("state reader and command applier are required")
	}
	return &Service{reader: reader, applier: applier, now: time.Now, newID: randomID}, nil
}

func (s *Service) ListNodes(actor Principal) ([]NodeItem, error) {
	if actor.UserID <= 0 {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	leaderID := domain.NodeID("")
	if s.leaders != nil {
		leaderID = domain.NodeID(s.leaders.LeaderID())
	}
	nodes := visibleNodes(state, actor, leaderID)
	items := make([]NodeItem, 0, len(nodes))
	for _, node := range nodes {
		count := 0
		for _, session := range state.Sessions {
			if session.NodeID == node.ID && session.IsLive() &&
				state.CanViewSession(actor.UserID, session.Ref()) {
				count++
			}
		}
		items = append(items, NodeItem{Node: node, LiveSessions: count})
	}
	return items, nil
}

func (s *Service) ListSessions(actor Principal) ([]SessionItem, error) {
	if actor.UserID <= 0 {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	preferences, ok := state.Preferences[actor.UserID]
	if !ok {
		return nil, domain.ErrAccessDenied
	}
	sessions := state.VisibleSessions(actor.UserID, true)
	if preferences.SessionView == domain.ViewHostFirst {
		selectedNode := state.Navigation.ActiveNodeByUser[actor.UserID]
		if selectedNode == "" {
			visible := state.VisibleNodes(actor.UserID)
			if len(visible) == 1 {
				selectedNode = visible[0].ID
			}
		}
		filtered := sessions[:0]
		for _, session := range sessions {
			if session.NodeID == selectedNode {
				filtered = append(filtered, session)
			}
		}
		sessions = filtered
	}
	items := make([]SessionItem, 0, len(sessions))
	for _, session := range sessions {
		access := domain.ShareView
		if state.CanControlSession(actor.UserID, session.Ref()) {
			access = domain.ShareControl
		}
		items = append(items, SessionItem{
			Session: session,
			Node:    state.Nodes[session.NodeID],
			Access:  access,
		})
	}
	return items, nil
}

func (s *Service) RequireSessionAction(
	actor Principal,
	ref domain.SessionRef,
	action domain.SessionAction,
) error {
	state := s.reader.State()
	if state == nil || !state.CanPerformSessionAction(actor.UserID, ref, action) {
		// Do not distinguish a private object from a missing object.
		return domain.ErrNotFound
	}
	if requiresOnlineRuntime(action) {
		session, ok := state.Sessions[ref.Key()]
		node, nodeOK := state.Nodes[ref.NodeID]
		if !ok || !session.IsLive() || !nodeOK || node.Status != domain.NodeOnline {
			return domain.ErrInvalidState
		}
	}
	return nil
}

func requiresOnlineRuntime(action domain.SessionAction) bool {
	switch action {
	case domain.ActionSendInput, domain.ActionSendKey, domain.ActionStop,
		domain.ActionClear, domain.ActionClose, domain.ActionOpenTerminal,
		domain.ActionCapture:
		return true
	case domain.ActionRestore:
		return false
	default:
		return false
	}
}

func (s *Service) ShareSession(
	ctx context.Context,
	actor Principal,
	ref domain.SessionRef,
	recipient domain.UserID,
	mode domain.ShareMode,
) error {
	// Bria currently has one trusted owner. Keep the legacy domain command
	// decodable for old snapshots, but do not expose multi-user sharing through
	// the application boundary until its security model is designed explicitly.
	return domain.ErrAccessDenied
}

func (s *Service) SetPreferences(
	ctx context.Context,
	actor Principal,
	preferences domain.UserPreferences,
) error {
	return s.apply(
		ctx,
		clusterstate.CommandSetPreferences,
		clusterstate.SetPreferences{UserID: actor.UserID, Preferences: preferences},
	)
}

func (s *Service) TelegramResponseCard(
	actor Principal,
) (domain.TelegramResponseCard, bool, error) {
	if actor.UserID <= 0 {
		return domain.TelegramResponseCard{}, false, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if _, ok := state.Users[actor.UserID]; !ok {
		return domain.TelegramResponseCard{}, false, domain.ErrAccessDenied
	}
	card, ok := state.TelegramResponseCards[actor.UserID]
	return card, ok, nil
}

func (s *Service) RecordTelegramResponseCard(
	ctx context.Context,
	actor Principal,
	card domain.TelegramResponseCard,
) error {
	return s.apply(ctx, clusterstate.CommandRecordTelegramCard, clusterstate.RecordTelegramCard{
		UserID: actor.UserID,
		Card:   card,
	})
}

func (s *Service) MarkBackgroundNotified(
	ctx context.Context,
	actor Principal,
	ref domain.SessionRef,
	eventRevision uint64,
) error {
	return s.apply(ctx, clusterstate.CommandMarkBackgroundNotified,
		clusterstate.MarkBackgroundNotified{
			UserID: actor.UserID, Session: ref, EventRevision: eventRevision,
		})
}

func (s *Service) SelectNode(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
) error {
	return s.apply(ctx, clusterstate.CommandSelectNode, clusterstate.SelectNode{
		UserID: actor.UserID,
		NodeID: nodeID,
	})
}

func (s *Service) SelectSession(
	ctx context.Context,
	actor Principal,
	ref domain.SessionRef,
) error {
	session, err := s.Session(actor, ref)
	if err != nil {
		return err
	}
	if !session.IsLive() {
		return domain.ErrInvalidState
	}
	return s.apply(ctx, clusterstate.CommandSelectSession, clusterstate.SelectSession{
		UserID:  actor.UserID,
		Session: ref,
	})
}

func (s *Service) apply(
	ctx context.Context,
	kind clusterstate.CommandKind,
	payload any,
) error {
	operationID := ""
	if scope, ok := ctx.Value(operationScopeKey{}).(string); ok && scope != "" {
		digest := sha256.Sum256([]byte(scope + "\x00" + string(kind)))
		operationID = "scoped-" + hex.EncodeToString(digest[:16])
	} else {
		var err error
		operationID, err = s.newID()
		if err != nil {
			return err
		}
	}
	command, err := clusterstate.NewCommand(operationID, kind, s.now(), payload)
	if err != nil {
		return err
	}
	result, err := s.applier.Apply(ctx, command)
	if err != nil {
		return err
	}
	return result.Err()
}

type operationScopeKey struct{}

// WithOperationScope makes command IDs deterministic inside one externally
// delivered event. Replaying the same Telegram update after leader failover
// therefore reuses the replicated operation ledger instead of duplicating a
// state transition.
func WithOperationScope(ctx context.Context, scope string) context.Context {
	if scope == "" {
		return ctx
	}
	return context.WithValue(ctx, operationScopeKey{}, scope)
}

func randomID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
