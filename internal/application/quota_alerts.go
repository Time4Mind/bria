package application

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type QuotaAlertObservation struct {
	Key          string
	UserID       domain.UserID
	Backend      string
	AccountLabel string
	Window       string
	UsedPercent  int
	ResetsAt     time.Time
	CollectedAt  time.Time
	NodeID       domain.NodeID
}

func (s *Service) QuotaAlertObservations() []QuotaAlertObservation {
	state := s.reader.State()
	if state == nil {
		return nil
	}
	ownerID := state.OwnerID()
	if ownerID == 0 {
		return nil
	}
	selected := make(map[string]QuotaAlertObservation)
	for _, snapshot := range state.Quotas {
		if !state.CanAccessNode(ownerID, snapshot.NodeID) {
			continue
		}
		accountKey, label := quotaAccountIdentity(state, snapshot)
		for window, value := range map[string]*domain.QuotaWindow{
			"5h": snapshot.FiveHour, "week": snapshot.Weekly,
		} {
			if value == nil {
				continue
			}
			key := strings.ToLower(snapshot.Backend) + "/" + accountKey + "/" + window
			candidate := QuotaAlertObservation{
				Key: key, UserID: ownerID, Backend: snapshot.Backend,
				AccountLabel: label, Window: window, UsedPercent: value.UsedPercent,
				ResetsAt: value.ResetsAt, CollectedAt: snapshot.CollectedAt,
				NodeID: snapshot.NodeID,
			}
			current, exists := selected[key]
			if !exists || candidate.CollectedAt.After(current.CollectedAt) ||
				(candidate.CollectedAt.Equal(current.CollectedAt) && candidate.NodeID < current.NodeID) {
				selected[key] = candidate
			}
		}
	}
	result := make([]QuotaAlertObservation, 0, len(selected))
	for _, observation := range selected {
		result = append(result, observation)
	}
	slices.SortFunc(result, func(a, b QuotaAlertObservation) int {
		return cmp.Compare(a.Key, b.Key)
	})
	return result
}

func quotaAccountIdentity(
	state *domain.State,
	snapshot domain.QuotaSnapshot,
) (string, string) {
	label := snapshot.AccountLabel
	if label == "" {
		label = snapshot.AccountID
	}
	if snapshot.AccountID != "" {
		return snapshot.AccountID, label
	}
	if alias := state.ProviderAccountAlias(snapshot.NodeID, snapshot.Backend); alias != "" {
		return "alias:" + strings.ToLower(alias), alias
	}
	nodeName := string(snapshot.NodeID)
	if node, ok := state.Nodes[snapshot.NodeID]; ok && node.Name != "" {
		nodeName = node.Name
	}
	return "node:" + string(snapshot.NodeID), nodeName
}
