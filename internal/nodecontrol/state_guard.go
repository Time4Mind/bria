package nodecontrol

import (
	"context"
	"crypto/x509"
	"errors"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/security"
)

type StateReader interface {
	State() *domain.State
}

// StateGuard re-authorizes every command on the target node. A leader-side UI
// check alone is insufficient because ACLs or shares may change in flight.
type StateGuard struct {
	reader StateReader
}

func NewStateGuard(reader StateReader) (*StateGuard, error) {
	if reader == nil {
		return nil, errors.New("state reader is required")
	}
	return &StateGuard{reader: reader}, nil
}

func (g *StateGuard) IsMember(nodeID string) bool {
	state := g.reader.State()
	if state == nil {
		return false
	}
	node, ok := state.Nodes[domain.NodeID(nodeID)]
	return ok && node.Enabled()
}

// InputQueueLimit exposes the same replicated preference to the owning
// runtime node. Authorization is still performed separately for every request.
func (g *StateGuard) InputQueueLimit(actorID int64) int {
	state := g.reader.State()
	if state == nil {
		return domain.DefaultUserPreferences().EffectiveOfflineInputQueueLimit()
	}
	preferences, ok := state.Preferences[domain.UserID(actorID)]
	if !ok {
		preferences = domain.DefaultUserPreferences()
	}
	return preferences.EffectiveOfflineInputQueueLimit()
}

func (g *StateGuard) AuthorizeCertificate(nodeID string, certificate *x509.Certificate) bool {
	state := g.reader.State()
	if state == nil {
		return false
	}
	node, ok := state.Nodes[domain.NodeID(nodeID)]
	if !ok || !node.Enabled() {
		return false
	}
	current, err := security.NodeCertificateFingerprint(certificate)
	if err != nil || node.Fingerprint == "" {
		return err == nil
	}
	if current == node.Fingerprint {
		return true
	}
	previous, present, err := security.PreviousNodeCertificateFingerprint(certificate)
	return err == nil && present && previous == node.Fingerprint
}

func (g *StateGuard) AuthorizeRuntime(
	_ context.Context,
	request runtimehost.Request,
) error {
	state := g.reader.State()
	if state == nil {
		return domain.ErrAccessDenied
	}
	ref := domain.SessionRef{
		NodeID: domain.NodeID(request.NodeID), SessionID: domain.SessionID(request.SessionID),
	}
	node, ok := state.Nodes[ref.NodeID]
	if !ok || !node.Enabled() || !node.BackendExecutionAllowed() {
		return domain.ErrAccessDenied
	}
	session, ok := state.Sessions[ref.Key()]
	if !ok || session.RuntimeGeneration != request.ExpectedGeneration {
		return domain.ErrNotFound
	}
	if request.Action == runtimehost.ActionClose {
		if session.State != domain.SessionArchived || session.ArchiveReason != domain.ArchiveManual ||
			session.ArchiveID == "" || session.ArchiveID != request.ArchiveCommitID {
			return domain.ErrAccessDenied
		}
	} else if request.Action == runtimehost.ActionDiscard {
		if session.State != domain.SessionDiscarding || request.ArchiveCommitID != "" ||
			request.Archive != nil {
			return domain.ErrAccessDenied
		}
	} else if !session.IsLive() {
		return domain.ErrNotFound
	}
	action, ownerOnly := domainAction(request.Action)
	if ownerOnly {
		if session.OwnerID != domain.UserID(request.ActorID) ||
			!state.CanAccessNode(domain.UserID(request.ActorID), ref.NodeID) {
			return domain.ErrAccessDenied
		}
		return nil
	}
	if action == "" || !state.CanPerformSessionAction(domain.UserID(request.ActorID), ref, action) {
		return domain.ErrAccessDenied
	}
	return nil
}

func (g *StateGuard) AuthorizeProviderAuth(
	_ context.Context,
	actorID int64,
	nodeID string,
	backend string,
) error {
	state := g.reader.State()
	if state == nil || state.OwnerID() != domain.UserID(actorID) {
		return domain.ErrAccessDenied
	}
	node, ok := state.Nodes[domain.NodeID(nodeID)]
	if !ok || !node.Enabled() || node.Status != domain.NodeOnline ||
		!node.BackendExecutionAllowed() ||
		!state.CanAccessNode(domain.UserID(actorID), node.ID) {
		return domain.ErrNotFound
	}
	for _, descriptor := range node.Backends {
		if strings.EqualFold(descriptor.Name, backend) {
			return nil
		}
	}
	return domain.ErrNotFound
}

func domainAction(action runtimehost.Action) (domain.SessionAction, bool) {
	switch action {
	case runtimehost.ActionSendInput:
		return domain.ActionSendInput, false
	case runtimehost.ActionSendKey:
		return domain.ActionSendKey, false
	case runtimehost.ActionStop:
		return domain.ActionStop, false
	case runtimehost.ActionCapture:
		return domain.ActionCapture, false
	case runtimehost.ActionClear:
		return domain.ActionClear, true
	case runtimehost.ActionClose, runtimehost.ActionDiscard:
		return domain.ActionClose, true
	case runtimehost.ActionOpenTerminal:
		return domain.ActionOpenTerminal, true
	case runtimehost.ActionGenerateName:
		return domain.ActionRename, true
	default:
		return "", false
	}
}

var _ Authorizer = (*StateGuard)(nil)
var _ Membership = (*StateGuard)(nil)
var _ CertificateMembership = (*StateGuard)(nil)
var _ runtimehost.InputQueueLimitResolver = (*StateGuard)(nil)
