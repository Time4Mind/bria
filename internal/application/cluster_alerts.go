package application

import "github.com/Time4Mind/bria/internal/domain"

// ClusterAlertTarget contains the minimum replicated data required for a
// node-local cluster connectivity notification. It deliberately excludes
// session data and credentials.
type ClusterAlertTarget struct {
	OwnerID  domain.UserID
	NodeName string
	Language domain.Language
	Enabled  bool
}

func (s *Service) ClusterAlertTarget(nodeID domain.NodeID) (ClusterAlertTarget, bool) {
	state := s.reader.State()
	if state == nil {
		return ClusterAlertTarget{}, false
	}
	node, ok := state.Nodes[nodeID]
	if !ok {
		return ClusterAlertTarget{}, false
	}
	ownerID := state.OwnerID()
	if ownerID <= 0 || !state.CanAccessNode(ownerID, nodeID) {
		return ClusterAlertTarget{}, false
	}
	preferences, ok := state.Preferences[ownerID]
	if !ok {
		return ClusterAlertTarget{}, false
	}
	name := node.Name
	if name == "" {
		name = string(node.ID)
	}
	return ClusterAlertTarget{
		OwnerID: ownerID, NodeName: name,
		Language: preferences.EffectiveLanguage(), Enabled: node.Enabled(),
	}, true
}
