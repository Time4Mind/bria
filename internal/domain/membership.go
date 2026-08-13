package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *State) IssueEnrollmentInvite(invite EnrollmentInvite, at time.Time) error {
	for id, current := range s.EnrollmentInvites {
		if !at.Before(current.ExpiresAt) {
			delete(s.EnrollmentInvites, id)
		}
	}
	if err := validateIdentifier("invitation id", invite.ID); err != nil {
		return err
	}
	if invite.SecretHash == "" || !invite.ExpiresAt.After(at) {
		return fmt.Errorf("%w: enrollment invitation is invalid", ErrInvalidState)
	}
	if _, exists := s.EnrollmentInvites[invite.ID]; exists {
		return ErrAlreadyExists
	}
	s.EnrollmentInvites[invite.ID] = invite
	return nil
}

func (s *State) SubmitEnrollment(
	request EnrollmentRequest,
	expectedHash string,
	at time.Time,
) error {
	if err := request.Validate(); err != nil {
		return err
	}
	invite, ok := s.EnrollmentInvites[request.InviteID]
	if !ok || invite.SecretHash != expectedHash || !invite.UsedAt.IsZero() ||
		!at.Before(invite.ExpiresAt) {
		return fmt.Errorf("%w: enrollment invitation is unavailable", ErrInvalidState)
	}
	if _, exists := s.EnrollmentRequests[request.ID]; exists {
		return ErrAlreadyExists
	}
	if _, exists := s.Nodes[request.NodeID]; exists {
		return ErrAlreadyExists
	}
	if _, deleted := s.NodeTombstones[request.NodeID]; deleted {
		return fmt.Errorf("%w: deleted node identity cannot rejoin", ErrInvalidState)
	}
	request.Name = s.UniqueNodeName(request.Name)
	request.Status = EnrollmentPending
	request.RequestedAt = at
	request.ExpiresAt = at.Add(24 * time.Hour)
	invite.UsedAt = at
	s.EnrollmentInvites[invite.ID] = invite
	s.EnrollmentRequests[request.ID] = request
	return nil
}

func (s *State) SubmitNodeContract(request EnrollmentRequest, at time.Time) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if _, exists := s.EnrollmentRequests[request.ID]; exists {
		return ErrAlreadyExists
	}
	if _, exists := s.Nodes[request.NodeID]; exists {
		return ErrAlreadyExists
	}
	if _, deleted := s.NodeTombstones[request.NodeID]; deleted {
		return fmt.Errorf("%w: deleted node identity cannot rejoin", ErrInvalidState)
	}
	request.InviteID = ""
	request.Name = s.UniqueNodeName(request.Name)
	request.Status = EnrollmentPending
	request.RequestedAt = at
	request.ExpiresAt = at.Add(24 * time.Hour)
	s.EnrollmentRequests[request.ID] = request
	return nil
}

func (s *State) DecideEnrollment(requestID string, approve bool, at time.Time) error {
	request, ok := s.EnrollmentRequests[requestID]
	if !ok {
		return ErrNotFound
	}
	if request.Status != EnrollmentPending || !at.Before(request.ExpiresAt) {
		return ErrInvalidState
	}
	request.DecidedAt = at
	if !approve {
		request.Status = EnrollmentRejected
		s.EnrollmentRequests[requestID] = request
		return nil
	}
	request.Name = s.UniqueNodeName(request.Name)
	if err := s.AddNode(Node{
		ID: request.NodeID, Name: request.Name, Status: NodeOffline,
		Lifecycle: NodeActive, Network: request.Network, OS: request.OS, Arch: request.Arch,
		Fingerprint: request.Fingerprint, CreatedAt: at,
	}); err != nil {
		return err
	}
	request.Status = EnrollmentApproved
	s.EnrollmentRequests[requestID] = request
	return nil
}

func (s *State) MarkEnrollmentNotified(requestID string, at time.Time) error {
	request, ok := s.EnrollmentRequests[requestID]
	if !ok {
		return ErrNotFound
	}
	if request.Status != EnrollmentPending {
		return ErrInvalidState
	}
	if request.NotifiedAt.IsZero() {
		request.NotifiedAt = at
		s.EnrollmentRequests[requestID] = request
	}
	return nil
}

func (s *State) RenameNode(nodeID NodeID, name string) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\r\n\t") {
		return errors.New("node name must contain 1 to 64 printable characters")
	}
	for otherID, other := range s.Nodes {
		if otherID != nodeID && strings.EqualFold(other.Name, name) {
			return ErrAlreadyExists
		}
	}
	node.Name = name
	s.Nodes[nodeID] = node
	return nil
}

func (s *State) UpdateNodeMetadata(update Node) error {
	node, ok := s.Nodes[update.ID]
	if !ok {
		return ErrNotFound
	}
	if update.Network.RaftAddress != "" {
		node.Network = update.Network
	}
	if update.OS != "" {
		node.OS = update.OS
	}
	if update.Arch != "" {
		node.Arch = update.Arch
	}
	if update.Fingerprint != "" {
		node.Fingerprint = update.Fingerprint
	}
	s.Nodes[update.ID] = node
	return nil
}

func (s *State) SetNodeLifecycle(nodeID NodeID, lifecycle NodeLifecycle) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	if lifecycle != NodeActive && lifecycle != NodeDisabled {
		return ErrInvalidState
	}
	if lifecycle == NodeDisabled {
		if err := s.CanDisableNode(nodeID); err != nil {
			return err
		}
	}
	node.Lifecycle = lifecycle
	if lifecycle == NodeDisabled {
		node.Status = NodeOffline
		if s.TemporaryLeader.NodeID == nodeID {
			s.TemporaryLeader = TemporaryLeader{}
		}
		for _, session := range s.Sessions {
			if session.NodeID == nodeID && session.IsLive() {
				s.repairNavigationAfterUnavailable(session.Ref())
			}
		}
		s.clearBackgroundNode(nodeID)
	}
	s.Nodes[nodeID] = node
	return nil
}

func (s *State) CanDisableNode(nodeID NodeID) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	if node.Enabled() && s.availableEnabledNodeCount(nodeID) == 0 {
		return fmt.Errorf("%w: cannot disable the final available node", ErrInvalidState)
	}
	return nil
}

func (s *State) DeleteDisabledNode(nodeID NodeID, at time.Time) error {
	node, ok := s.Nodes[nodeID]
	if !ok {
		return ErrNotFound
	}
	if node.EffectiveLifecycle() != NodeDisabled {
		return fmt.Errorf("%w: disable node before deletion", ErrInvalidState)
	}
	delete(s.Nodes, nodeID)
	s.NodeTombstones[nodeID] = NodeTombstone{
		NodeID: nodeID, Fingerprint: node.Fingerprint, DeletedAt: at,
	}
	for key, quota := range s.Quotas {
		if quota.NodeID == nodeID {
			delete(s.Quotas, key)
		}
	}
	for userID, access := range s.Users {
		delete(access.AllowedNodes, nodeID)
		s.Users[userID] = access
		if s.Navigation.ActiveNodeByUser[userID] == nodeID {
			delete(s.Navigation.ActiveNodeByUser, userID)
		}
		delete(s.Navigation.ActiveSessionByUserNode[userID], nodeID)
	}
	return nil
}

func (s *State) UniqueNodeName(wanted string) string {
	base := strings.TrimSpace(wanted)
	if base == "" {
		base = "Node"
	}
	used := func(candidate string) bool {
		for _, node := range s.Nodes {
			if strings.EqualFold(node.Name, candidate) {
				return true
			}
		}
		return false
	}
	if !used(base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s %d", base, suffix)
		if len(candidate) <= 64 && !used(candidate) {
			return candidate
		}
	}
}

func (s *State) availableEnabledNodeCount(except NodeID) int {
	count := 0
	for nodeID, node := range s.Nodes {
		if nodeID != except && node.Enabled() &&
			(node.Status == NodeOnline || node.Status == NodeReconnecting) {
			count++
		}
	}
	return count
}

func (s *State) cloneMembership(clone *State) {
	for id, invite := range s.EnrollmentInvites {
		clone.EnrollmentInvites[id] = invite
	}
	for id, request := range s.EnrollmentRequests {
		clone.EnrollmentRequests[id] = request
	}
	for id, tombstone := range s.NodeTombstones {
		clone.NodeTombstones[id] = tombstone
	}
}
