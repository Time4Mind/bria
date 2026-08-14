package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/security"
)

const enrollmentInviteTTL = 30 * time.Minute

type EnrollmentInvitationConfig struct {
	ClusterID     string
	IssuerNodeID  string
	Endpoint      string
	CACertificate string
}

func (s *Service) SetEnrollmentInvitationConfig(config EnrollmentInvitationConfig) error {
	if config.ClusterID == "" || config.IssuerNodeID == "" || config.Endpoint == "" ||
		config.CACertificate == "" {
		return errors.New("enrollment invitation configuration is incomplete")
	}
	s.enrollment = config
	return nil
}

func (s *Service) CreateEnrollmentInvitation(
	ctx context.Context,
	actor Principal,
) (string, time.Time, error) {
	if !s.IsOwner(actor) {
		return "", time.Time{}, domain.ErrAccessDenied
	}
	if s.enrollment.ClusterID == "" {
		return "", time.Time{}, errors.New("enrollment is unavailable on this node")
	}
	issuer, ok := s.reader.State().Nodes[domain.NodeID(s.enrollment.IssuerNodeID)]
	if !ok || !issuer.Enabled() || issuer.Status == domain.NodeOffline {
		return "", time.Time{}, errors.New("enrollment issuer is unavailable")
	}
	id, err := s.newID()
	if err != nil {
		return "", time.Time{}, err
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return "", time.Time{}, err
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	expiresAt := s.now().UTC().Add(enrollmentInviteTTL)
	command, err := clusterstate.NewCommand(
		"enrollment-invite-"+id, clusterstate.CommandIssueEnrollmentInvite, s.now(),
		domain.EnrollmentInvite{
			ID: id, SecretHash: domain.HashEnrollmentSecret(secret), ExpiresAt: expiresAt,
		},
	)
	if err != nil {
		return "", time.Time{}, err
	}
	result, err := s.applier.Apply(ctx, command)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := result.Err(); err != nil {
		return "", time.Time{}, err
	}
	encoded, err := security.EncodeClusterInvitation(security.ClusterInvitation{
		Version: 1, ClusterID: s.enrollment.ClusterID, IssuerNodeID: s.enrollment.IssuerNodeID,
		Endpoint: s.enrollment.Endpoint, TokenID: id, Secret: secret,
		CACertificate: s.enrollment.CACertificate, ExpiresAt: expiresAt,
	})
	return encoded, expiresAt, err
}

func (s *Service) PendingEnrollments(actor Principal) ([]domain.EnrollmentRequest, error) {
	if !s.IsOwner(actor) {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	result := make([]domain.EnrollmentRequest, 0)
	for _, request := range state.EnrollmentRequests {
		if request.Status == domain.EnrollmentPending && s.now().Before(request.ExpiresAt) {
			result = append(result, request)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].RequestedAt.Before(result[j].RequestedAt)
	})
	return result, nil
}

func (s *Service) EnrollmentRequest(
	actor Principal,
	requestID string,
) (domain.EnrollmentRequest, error) {
	if !s.IsOwner(actor) {
		return domain.EnrollmentRequest{}, domain.ErrAccessDenied
	}
	request, ok := s.reader.State().EnrollmentRequests[requestID]
	if !ok {
		return domain.EnrollmentRequest{}, domain.ErrNotFound
	}
	return request, nil
}

func (s *Service) EnrollmentNotifications() (domain.UserID, []domain.EnrollmentRequest) {
	state := s.reader.State()
	if state == nil {
		return 0, nil
	}
	ownerID := state.OwnerID()
	result := make([]domain.EnrollmentRequest, 0)
	for _, request := range state.EnrollmentRequests {
		if request.Status == domain.EnrollmentPending && request.NotifiedAt.IsZero() &&
			s.now().Before(request.ExpiresAt) {
			result = append(result, request)
		}
	}
	return ownerID, result
}

func (s *Service) MarkEnrollmentNotified(
	ctx context.Context,
	actor Principal,
	requestID string,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandMarkEnrollmentNotified,
		clusterstate.MarkEnrollmentNotified{RequestID: requestID})
}

func (s *Service) DecideEnrollment(
	ctx context.Context,
	actor Principal,
	requestID string,
	approve bool,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandDecideEnrollment, clusterstate.DecideEnrollment{
		RequestID: requestID, Approve: approve,
	})
}

func (s *Service) SubmitNodeContract(
	ctx context.Context,
	actor Principal,
	encoded string,
) (domain.EnrollmentRequest, error) {
	if !s.IsOwner(actor) {
		return domain.EnrollmentRequest{}, domain.ErrAccessDenied
	}
	contract, err := security.DecodeNodeContract(encoded, s.now())
	if err != nil {
		return domain.EnrollmentRequest{}, err
	}
	request := contract.EnrollmentRequest(s.now())
	if err := s.apply(ctx, clusterstate.CommandSubmitNodeContract, request); err != nil {
		return domain.EnrollmentRequest{}, err
	}
	return request, nil
}

func (s *Service) EnrollmentClaim(actor Principal, requestID string) (string, error) {
	if !s.IsOwner(actor) {
		return "", domain.ErrAccessDenied
	}
	state := s.reader.State()
	request, ok := state.EnrollmentRequests[requestID]
	if !ok || request.Status != domain.EnrollmentApproved || request.InviteID != "" {
		return "", domain.ErrNotFound
	}
	return security.EncodeEnrollmentClaim(security.EnrollmentClaim{
		Version: 1, ClusterID: s.enrollment.ClusterID, IssuerNodeID: s.enrollment.IssuerNodeID,
		Endpoint: s.enrollment.Endpoint, RequestID: requestID,
		CACertificate: s.enrollment.CACertificate, ExpiresAt: request.ExpiresAt,
	})
}

func (s *Service) RenameNode(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
	name string,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandRenameNode, clusterstate.RenameNode{
		NodeID: nodeID, Name: name,
	})
}

func (s *Service) SetProviderAccountAlias(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
	backend string,
	alias string,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandSetProviderAlias, clusterstate.SetProviderAlias{
		NodeID: nodeID, Backend: backend, Alias: alias,
	})
}

func (s *Service) SetNodeBackendConnected(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
	backend string,
	connected bool,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandSetNodeBackend, clusterstate.SetNodeBackend{
		NodeID: nodeID, Backend: backend, Connected: connected,
	})
}

func (s *Service) SetNodeBackendIsolationRequired(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
	required bool,
) error {
	if !s.IsAdmin(actor) {
		return domain.ErrAccessDenied
	}
	state := s.reader.State()
	node, ok := state.Nodes[nodeID]
	if !ok || !state.CanAccessNode(actor.UserID, nodeID) {
		return domain.ErrNotFound
	}
	if required && !node.BackendIsolation.Ready {
		for _, session := range state.Sessions {
			if session.NodeID == nodeID && session.IsLive() {
				return domain.ErrInvalidState
			}
		}
	}
	return s.apply(ctx, clusterstate.CommandSetNodeIsolation, clusterstate.SetNodeIsolation{
		NodeID: nodeID, Required: required,
	})
}

type ProviderAliasCandidate struct {
	NodeID  domain.NodeID
	Backend string
}

func (s *Service) ProviderAliasCandidates(actor Principal) ([]ProviderAliasCandidate, error) {
	if !s.IsOwner(actor) {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	result := make([]ProviderAliasCandidate, 0)
	for _, node := range state.VisibleNodes(actor.UserID) {
		if !node.BackendExecutionAllowed() {
			continue
		}
		for _, backend := range node.Backends {
			if strings.TrimSpace(backend.Name) != "" {
				result = append(result, ProviderAliasCandidate{
					NodeID: node.ID, Backend: backend.Name,
				})
			}
		}
	}
	return result, nil
}

func (s *Service) SetNodeEnabled(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
	enabled bool,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	lifecycle := domain.NodeDisabled
	if enabled {
		lifecycle = domain.NodeActive
	}
	return s.apply(ctx, clusterstate.CommandSetNodeLifecycle, clusterstate.SetNodeLifecycle{
		NodeID: nodeID, Lifecycle: lifecycle,
	})
}

func (s *Service) CanDisableNode(actor Principal, nodeID domain.NodeID) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.reader.State().CanDisableNode(nodeID)
}

func (s *Service) DeleteNode(
	ctx context.Context,
	actor Principal,
	nodeID domain.NodeID,
) error {
	if !s.IsOwner(actor) {
		return domain.ErrAccessDenied
	}
	return s.apply(ctx, clusterstate.CommandDeleteNode, clusterstate.DeleteNode{NodeID: nodeID})
}

func (s *Service) LiveSessionsOnNode(
	actor Principal,
	nodeID domain.NodeID,
) ([]domain.Session, error) {
	if !s.IsOwner(actor) {
		return nil, domain.ErrAccessDenied
	}
	state := s.reader.State()
	if _, ok := state.Nodes[nodeID]; !ok {
		return nil, domain.ErrNotFound
	}
	result := make([]domain.Session, 0)
	for _, session := range state.Sessions {
		if session.NodeID == nodeID && session.IsLive() {
			result = append(result, session)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}
