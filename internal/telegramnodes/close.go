package telegramnodes

import (
	"context"

	"bria/internal/domain"
)

// ClosedSelection describes node-local cleanup; Selected remains the user's
// foreground node even when the closed session belongs to another node.
type ClosedSelection struct {
	Selected                     domain.ComputerID
	Previous, Active             domain.SessionID
	CandidatesKnown              bool
	CandidateCount               uint64
	Recent, Preserved, Persisted bool
}

type closedSelectionStore interface {
	SetNodeLastActiveSession(context.Context, domain.ComputerID, domain.SessionID) error
}

// PersistsSelection describes the user-selection write capability, not readiness.
func (scope *Scope) PersistsSelection() bool {
	_, modern := scope.ui.(selectionStore)
	_, legacy := scope.ui.(legacyActiveStore)
	return modern || legacy
}

// RemoveClosed resolves against a complete durable snapshot and persists before
// changing the scope cache. A failed write remains retryable. Callers serialize
// this with user navigation, including the selected-node persistence boundary.
func (scope *Scope) RemoveClosed(ctx context.Context, session domain.Session) (result ClosedSelection, err error) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	node := session.ComputerID()
	result.Selected, result.Previous = scope.selected, scope.active[node]
	sessions, err := scope.sessions.List(ctx)
	if err != nil {
		return result, err
	}
	result.CandidatesKnown = true
	candidates := make(map[domain.SessionID]domain.Session)
	var latest domain.Session
	for _, candidate := range sessions {
		if candidate.ID() == session.ID() || candidate.ComputerID() != node || !selectableStatus(candidate.Status()) {
			continue
		}
		candidates[candidate.ID()] = candidate
		result.CandidateCount++
		if latest.ID() == "" || candidate.StateChangedAt().After(latest.StateChangedAt()) || (candidate.StateChangedAt().Equal(latest.StateChangedAt()) && candidate.ID() < latest.ID()) {
			latest = candidate
		}
	}
	history := remove(scope.recent[node], session.ID())
	if _, valid := candidates[result.Previous]; valid {
		result.Active, result.Preserved = result.Previous, true
	} else {
		for _, id := range history {
			if _, valid := candidates[id]; valid {
				result.Active, result.Recent = id, true
				break
			}
		}
		if result.Active == "" {
			result.Active = latest.ID()
		}
	}
	if store, ok := scope.ui.(closedSelectionStore); ok && result.Active != "" {
		err = store.SetNodeLastActiveSession(ctx, node, result.Active)
		result.Persisted = err == nil
	} else if store, ok := scope.ui.(selectionStore); ok && (node == result.Selected || result.Active == "") {
		if result.Active == "" {
			err = store.ClearNodeActiveSession(ctx, node)
		} else {
			err = store.SetNodeActiveSession(ctx, node, result.Active)
		}
		result.Persisted = err == nil
	} else if store, ok := scope.ui.(legacyActiveStore); ok && node == result.Selected {
		err = store.SetActiveSession(ctx, result.Active)
		result.Persisted = err == nil
	}
	if err != nil {
		return result, err
	}
	scope.recent[node] = history
	if result.Active == "" {
		delete(scope.active, node)
	} else {
		scope.active[node] = result.Active
		scope.recent[node] = promote(history, result.Active)
	}
	return result, nil
}
