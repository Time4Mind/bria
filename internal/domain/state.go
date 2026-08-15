package domain

import (
	"fmt"
	"strings"
	"time"
)

const StateSchemaVersion = 1

type Navigation struct {
	ActiveNodeByUser        map[UserID]NodeID                      `json:"active_node_by_user"`
	ActiveSessionByUserNode map[UserID]map[NodeID]SessionID        `json:"active_session_by_user_node"`
	SessionActivityByUser   map[UserID]map[string]time.Time        `json:"session_activity_by_user,omitempty"`
	BackgroundByUser        map[UserID]map[string]BackgroundNotice `json:"background_by_user,omitempty"`
}
type State struct {
	SchemaVersion           int                               `json:"schema_version"`
	Nodes                   map[NodeID]Node                   `json:"nodes"`
	Sessions                map[string]Session                `json:"sessions"`
	Users                   map[UserID]UserAccess             `json:"users"`
	Grants                  map[string]SessionGrant           `json:"grants"`
	Preferences             map[UserID]UserPreferences        `json:"preferences"`
	Navigation              Navigation                        `json:"navigation"`
	TelegramResponseCards   map[UserID]TelegramResponseCard   `json:"telegram_response_cards,omitempty"`
	Quotas                  map[string]QuotaSnapshot          `json:"quotas,omitempty"`
	ProviderAccountAliases  map[string]string                 `json:"provider_account_aliases,omitempty"`
	QuotaRefreshRequestedAt time.Time                         `json:"quota_refresh_requested_at,omitempty"`
	LeaderPolicy            LeaderPolicy                      `json:"leader_policy,omitempty"`
	TemporaryLeader         TemporaryLeader                   `json:"temporary_leader,omitempty"`
	TelegramBotID           int64                             `json:"telegram_bot_id,omitempty"`
	TelegramNextUpdateID    int64                             `json:"telegram_next_update_id,omitempty"`
	EnrollmentInvites       map[string]EnrollmentInvite       `json:"enrollment_invites,omitempty"`
	EnrollmentRequests      map[string]EnrollmentRequest      `json:"enrollment_requests,omitempty"`
	NodeTombstones          map[NodeID]NodeTombstone          `json:"node_tombstones,omitempty"`
	DeferredInputs          map[string][]DeferredSessionInput `json:"deferred_inputs,omitempty"`
	ClusterUpdate           *ClusterUpdate                    `json:"cluster_update,omitempty"`
}

func NewState() *State {
	return &State{
		SchemaVersion:          StateSchemaVersion,
		Nodes:                  make(map[NodeID]Node),
		Sessions:               make(map[string]Session),
		Users:                  make(map[UserID]UserAccess),
		Grants:                 make(map[string]SessionGrant),
		Preferences:            make(map[UserID]UserPreferences),
		TelegramResponseCards:  make(map[UserID]TelegramResponseCard),
		Quotas:                 make(map[string]QuotaSnapshot),
		ProviderAccountAliases: make(map[string]string),
		EnrollmentInvites:      make(map[string]EnrollmentInvite),
		EnrollmentRequests:     make(map[string]EnrollmentRequest),
		NodeTombstones:         make(map[NodeID]NodeTombstone),
		DeferredInputs:         make(map[string][]DeferredSessionInput),
		Navigation: Navigation{
			ActiveNodeByUser:        make(map[UserID]NodeID),
			ActiveSessionByUserNode: make(map[UserID]map[NodeID]SessionID),
			SessionActivityByUser:   make(map[UserID]map[string]time.Time),
			BackgroundByUser:        make(map[UserID]map[string]BackgroundNotice),
		},
	}
}

func (s *State) AddNode(node Node) error {
	if s.ClusterUpdate != nil && s.ClusterUpdate.Active() {
		return fmt.Errorf("%w: cluster membership is locked during update", ErrInvalidState)
	}
	if err := validateIdentifier("node id", string(node.ID)); err != nil {
		return err
	}
	node.Name = strings.TrimSpace(node.Name)
	if node.Name == "" || len(node.Name) > 64 || strings.ContainsAny(node.Name, "\r\n\t") {
		return fmt.Errorf("node name must contain 1 to 64 printable characters")
	}
	if _, exists := s.Nodes[node.ID]; exists {
		return ErrAlreadyExists
	}
	if _, deleted := s.NodeTombstones[node.ID]; deleted {
		return fmt.Errorf("%w: node identity was deleted", ErrInvalidState)
	}
	if node.Lifecycle == "" {
		node.Lifecycle = NodeActive
	}
	s.Nodes[node.ID] = node
	for userID, access := range s.Users {
		if access.Role == RoleOwner {
			if access.AllowedNodes == nil {
				access.AllowedNodes = make(map[NodeID]bool)
			}
			access.AllowedNodes[node.ID] = true
			s.Users[userID] = access
		}
	}
	return nil
}

func (s *State) SetNodeAccess(userID UserID, role Role, nodeIDs ...NodeID) error {
	if userID <= 0 {
		return fmt.Errorf("user id must be positive")
	}
	if role != RoleOwner && role != RoleAdmin && role != RoleMember {
		return fmt.Errorf("unsupported role: %q", role)
	}
	allowed := make(map[NodeID]bool, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if _, ok := s.Nodes[nodeID]; !ok {
			return fmt.Errorf("%w: node %q", ErrNotFound, nodeID)
		}
		allowed[nodeID] = true
	}
	s.Users[userID] = UserAccess{Role: role, AllowedNodes: allowed}
	if _, ok := s.Preferences[userID]; !ok {
		s.Preferences[userID] = DefaultUserPreferences()
	}
	s.ensureActiveSession(userID)
	for key, notice := range s.Navigation.BackgroundByUser[userID] {
		if !s.CanViewSession(userID, notice.Session) {
			delete(s.Navigation.BackgroundByUser[userID], key)
		}
	}
	return nil
}

func (s *State) AddSession(session Session) error {
	if err := session.Ref().Validate(); err != nil {
		return err
	}
	if session.OwnerID <= 0 {
		return fmt.Errorf("session owner is required")
	}
	if !s.CanAccessNode(session.OwnerID, session.NodeID) {
		return ErrAccessDenied
	}
	if err := normalizeNewSession(&session); err != nil {
		return err
	}
	if !session.IsLive() {
		return fmt.Errorf("%w: a new session must be live", ErrInvalidState)
	}
	key := session.Ref().Key()
	if _, exists := s.Sessions[key]; exists {
		return ErrAlreadyExists
	}
	s.Sessions[key] = session
	s.ensureActiveSession(session.OwnerID)
	return nil
}

func (s *State) VisibleNodes(userID UserID) []Node {
	result := make([]Node, 0)
	for nodeID, node := range s.Nodes {
		if s.CanAccessNode(userID, nodeID) {
			result = append(result, node)
		}
	}
	return result
}

func (s *State) VisibleSessions(userID UserID, liveOnly bool) []Session {
	result := make([]Session, 0)
	for _, session := range s.Sessions {
		if !s.CanViewSession(userID, session.Ref()) {
			continue
		}
		if liveOnly {
			if !session.IsLive() {
				continue
			}
		} else if session.State != SessionArchived && session.State != SessionLost {
			continue
		}
		result = append(result, session)
	}
	if liveOnly {
		SortLive(result)
	} else {
		SortArchived(result)
	}
	return result
}

func (s *State) SetPreferences(userID UserID, preferences UserPreferences) error {
	if _, ok := s.Users[userID]; !ok {
		return ErrNotFound
	}
	if err := preferences.Validate(); err != nil {
		return err
	}
	preferences.HiddenCardEvents = canonicalHiddenCardEvents(preferences.HiddenCardEvents)
	preferences.MutedBackgroundNotifications = canonicalMutedBackgroundNotifications(
		preferences.MutedBackgroundNotifications,
	)
	s.Preferences[userID] = preferences
	for key, notice := range s.Navigation.BackgroundByUser[userID] {
		if notice.Acknowledgements >= preferences.EffectiveBackgroundDismissSwitches() {
			notice.Dismissed = true
			s.Navigation.BackgroundByUser[userID][key] = notice
		}
	}
	return nil
}

func (s *State) Clone() *State {
	clone := NewState()
	clone.SchemaVersion = s.SchemaVersion
	clone.TelegramBotID = s.TelegramBotID
	clone.TelegramNextUpdateID = s.TelegramNextUpdateID
	for userID, card := range s.TelegramResponseCards {
		clone.TelegramResponseCards[userID] = card
	}
	for key, snapshot := range s.Quotas {
		clone.Quotas[key] = cloneQuota(snapshot)
	}
	for key, alias := range s.ProviderAccountAliases {
		clone.ProviderAccountAliases[key] = alias
	}
	clone.QuotaRefreshRequestedAt = s.QuotaRefreshRequestedAt
	clone.LeaderPolicy = s.LeaderPolicy
	clone.TemporaryLeader = s.TemporaryLeader
	if s.ClusterUpdate != nil {
		update := cloneClusterUpdate(*s.ClusterUpdate)
		clone.ClusterUpdate = &update
	}
	s.cloneMembership(clone)
	for id, node := range s.Nodes {
		node.Backends = cloneBackendDescriptors(node.Backends)
		node.InstalledBackends = cloneBackendDescriptors(node.InstalledBackends)
		clone.Nodes[id] = node
	}
	for key, session := range s.Sessions {
		if session.InteractivePrompt != nil {
			prompt := *session.InteractivePrompt
			session.InteractivePrompt = &prompt
		}
		if session.LastOperation != nil {
			result := *session.LastOperation
			session.LastOperation = &result
		}
		clone.Sessions[key] = session
	}
	for key, queue := range s.DeferredInputs {
		clone.DeferredInputs[key] = append([]DeferredSessionInput(nil), queue...)
	}
	for userID, access := range s.Users {
		allowed := make(map[NodeID]bool, len(access.AllowedNodes))
		for nodeID, enabled := range access.AllowedNodes {
			allowed[nodeID] = enabled
		}
		access.AllowedNodes = allowed
		clone.Users[userID] = access
	}
	for key, grant := range s.Grants {
		clone.Grants[key] = grant
	}
	for userID, preferences := range s.Preferences {
		clone.Preferences[userID] = preferences.clone()
	}
	for userID, nodeID := range s.Navigation.ActiveNodeByUser {
		clone.Navigation.ActiveNodeByUser[userID] = nodeID
	}
	for userID, perNode := range s.Navigation.ActiveSessionByUserNode {
		copyPerNode := make(map[NodeID]SessionID, len(perNode))
		for nodeID, sessionID := range perNode {
			copyPerNode[nodeID] = sessionID
		}
		clone.Navigation.ActiveSessionByUserNode[userID] = copyPerNode
	}
	for userID, activity := range s.Navigation.SessionActivityByUser {
		copyActivity := make(map[string]time.Time, len(activity))
		for key, at := range activity {
			copyActivity[key] = at
		}
		clone.Navigation.SessionActivityByUser[userID] = copyActivity
	}
	s.cloneBackgroundNotices(clone)
	clone.normalizeSessions()
	return clone
}

func (s *State) normalizeDeferredInputs() {
	if s.DeferredInputs == nil {
		s.DeferredInputs = make(map[string][]DeferredSessionInput)
	}
	for key, queue := range s.DeferredInputs {
		session, ok := s.Sessions[key]
		if !ok || !session.IsLive() || len(queue) == 0 {
			delete(s.DeferredInputs, key)
		}
	}
}
