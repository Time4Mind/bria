package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

const createCapability = "session.create"

type CreateSessionRequest struct {
	NodeID            domain.NodeID
	Backend           string
	Workdir           string
	ProviderSessionID string
}

func (s *Service) SelectedNode(actor Principal) (NodeItem, bool, error) {
	if actor.UserID <= 0 {
		return NodeItem{}, false, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if _, ok := state.Users[actor.UserID]; !ok {
		return NodeItem{}, false, domain.ErrAccessDenied
	}
	nodeID := state.Navigation.ActiveNodeByUser[actor.UserID]
	if nodeID == "" || !state.CanAccessNode(actor.UserID, nodeID) {
		return NodeItem{}, false, nil
	}
	node, ok := state.Nodes[nodeID]
	if !ok {
		return NodeItem{}, false, nil
	}
	count := 0
	for _, session := range state.Sessions {
		if session.NodeID == nodeID && session.IsLive() && state.CanViewSession(actor.UserID, session.Ref()) {
			count++
		}
	}
	return NodeItem{Node: node, LiveSessions: count}, true, nil
}

func (s *Service) CreateSession(
	ctx context.Context,
	actor Principal,
	request CreateSessionRequest,
) (domain.Session, error) {
	state := s.reader.State()
	node, ok := state.Nodes[request.NodeID]
	backend := strings.ToLower(strings.TrimSpace(request.Backend))
	if actor.UserID <= 0 || !state.CanAccessNode(actor.UserID, request.NodeID) || !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	if !node.Enabled() || node.Status != domain.NodeOnline || !node.BackendExecutionAllowed() ||
		!supportsBackend(node, backend, createCapability) {
		return domain.Session{}, domain.ErrInvalidState
	}
	if strings.TrimSpace(request.ProviderSessionID) != "" &&
		!supportsBackend(node, backend, "session.resume") {
		return domain.Session{}, domain.ErrInvalidState
	}
	workdir := filepath.Clean(strings.TrimSpace(request.Workdir))
	if !filepath.IsAbs(workdir) || strings.ContainsRune(workdir, 0) {
		return domain.Session{}, domain.ErrInvalidState
	}
	id, err := s.sessionID(ctx)
	if err != nil {
		return domain.Session{}, err
	}
	now := s.now().UTC()
	providerID := strings.TrimSpace(request.ProviderSessionID)
	resume := providerID != ""
	if backend == "claude" && providerID == "" {
		providerID, err = s.assignedProviderID(ctx)
		if err != nil {
			return domain.Session{}, err
		}
	}
	session := domain.Session{
		ID: domain.SessionID(id), NodeID: request.NodeID, OwnerID: actor.UserID,
		Workdir: workdir, Backend: backend,
		ProviderSessionID: providerID, ProviderResume: resume,
		ProviderBindingSince: now,
		State:                domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
		RuntimeGeneration: 1, Revision: 1,
		UserRequestTracked: !resume,
		CreatedAt:          now, LiveSinceAt: now, LastEventAt: now,
	}
	if err := s.apply(ctx, clusterstate.CommandAddSession, session); err != nil {
		return domain.Session{}, err
	}
	selectCtx := WithOperationScope(ctx, id+"-select")
	if err := s.SelectSession(selectCtx, actor, session.Ref()); err != nil {
		return domain.Session{}, err
	}
	return session, nil
}

func (s *Service) assignedProviderID(ctx context.Context) (string, error) {
	providerCtx := ctx
	if scope, ok := ctx.Value(operationScopeKey{}).(string); ok && scope != "" {
		providerCtx = WithOperationScope(ctx, scope+"-provider")
	}
	raw, err := s.sessionID(providerCtx)
	if err != nil {
		return "", err
	}
	return raw[0:8] + "-" + raw[8:12] + "-4" + raw[13:16] + "-a" + raw[17:20] + "-" + raw[20:32], nil
}

func (s *Service) BindProviderSession(
	ctx context.Context,
	actor Principal,
	session domain.Session,
	providerID string,
) error {
	return s.apply(ctx, clusterstate.CommandBindProviderSession, clusterstate.BindProviderSession{
		ActorID: actor.UserID, Session: session.Ref(), ExpectedRevision: session.Revision,
		ProviderID: providerID,
	})
}

func supportsBackend(node domain.Node, backend, capability string) bool {
	for _, candidate := range node.Backends {
		if strings.EqualFold(candidate.Name, backend) {
			for _, item := range candidate.Capabilities {
				if item == capability {
					return true
				}
			}
		}
	}
	return false
}

func (s *Service) sessionID(ctx context.Context) (string, error) {
	if scope, ok := ctx.Value(operationScopeKey{}).(string); ok && scope != "" {
		digest := sha256.Sum256([]byte("session\x00" + scope))
		return hex.EncodeToString(digest[:16]), nil
	}
	return s.newID()
}
