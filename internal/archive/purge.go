package archive

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

type PurgeCandidate struct {
	Manifest Manifest
	Policy   RetentionPolicy
}

type PurgeDecision struct {
	ArchiveID ArchiveID                  `json:"archive_id"`
	Session   domain.SessionRef          `json:"session"`
	OwnerID   domain.UserID              `json:"owner_id"`
	DueAt     time.Time                  `json:"due_at"`
	Action    domain.ArchiveExpiryAction `json:"action"`
}

// PlanPurge is deterministic and performs no I/O. Invalid or duplicate input
// is rejected instead of producing a partial plan.
func PlanPurge(now time.Time, candidates []PurgeCandidate) ([]PurgeDecision, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("planner timestamp is required")
	}
	seen := make(map[ArchiveID]bool, len(candidates))
	decisions := make([]PurgeDecision, 0, len(candidates))
	for _, candidate := range candidates {
		if err := candidate.Manifest.Validate(); err != nil {
			return nil, fmt.Errorf("invalid purge candidate: %w", err)
		}
		if seen[candidate.Manifest.ID] {
			return nil, fmt.Errorf("duplicate archive id: %s", candidate.Manifest.ID)
		}
		seen[candidate.Manifest.ID] = true
		dueAt, finite, err := candidate.Policy.DueAt(candidate.Manifest.ArchivedAt)
		if err != nil {
			return nil, fmt.Errorf("invalid purge policy: %w", err)
		}
		if !finite || now.Before(dueAt) {
			continue
		}
		decisions = append(decisions, PurgeDecision{
			ArchiveID: candidate.Manifest.ID,
			Session:   candidate.Manifest.Session,
			OwnerID:   candidate.Manifest.OwnerID,
			DueAt:     dueAt,
			Action:    candidate.Policy.Action,
		})
	}
	slices.SortFunc(decisions, func(a, b PurgeDecision) int {
		if order := a.DueAt.Compare(b.DueAt); order != 0 {
			return order
		}
		if order := cmp.Compare(a.Session.Key(), b.Session.Key()); order != 0 {
			return order
		}
		return cmp.Compare(a.ArchiveID, b.ArchiveID)
	})
	return decisions, nil
}
