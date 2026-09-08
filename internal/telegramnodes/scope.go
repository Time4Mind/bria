// Package telegramnodes owns Telegram's selected-node state and projection.
package telegramnodes

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
)

type (
	SessionStore interface {
		List(context.Context) ([]domain.Session, error)
		Load(context.Context, domain.SessionID) (domain.Session, error)
	}
	selectionLoader interface {
		LoadNodeSelection(context.Context) (domain.ComputerID, map[domain.ComputerID]domain.SessionID, error)
	}
	historyLoader interface {
		LoadNodeSessionHistory(context.Context) (map[domain.ComputerID][]domain.SessionID, error)
	}
	legacyActiveLoader interface {
		LoadActiveSession(context.Context) (domain.SessionID, error)
	}
	selectionStore interface {
		SetSelectedNode(context.Context, domain.ComputerID) error
		SetNodeActiveSession(context.Context, domain.ComputerID, domain.SessionID) error
		ClearNodeActiveSession(context.Context, domain.ComputerID) error
	}
	legacyActiveStore interface {
		SetActiveSession(context.Context, domain.SessionID) error
	}
)

type Scope struct {
	local    domain.ComputerID
	sessions SessionStore
	ui       any
	env      sessioncreation.Environment

	mu       sync.Mutex
	selected domain.ComputerID
	active   map[domain.ComputerID]domain.SessionID
	recent   map[domain.ComputerID][]domain.SessionID
}

func New(local domain.ComputerID, sessions SessionStore, ui any, environment sessioncreation.Environment) (*Scope, error) {
	if strings.TrimSpace(string(local)) == "" || sessions == nil {
		return nil, errors.New("local node and session store are required")
	}
	return &Scope{local: local, sessions: sessions, ui: ui, env: environment,
		selected: local, active: make(map[domain.ComputerID]domain.SessionID), recent: make(map[domain.ComputerID][]domain.SessionID)}, nil
}

func (scope *Scope) Restore(ctx context.Context) (domain.SessionID, error) {
	selected := scope.local
	active := make(map[domain.ComputerID]domain.SessionID)
	recent := make(map[domain.ComputerID][]domain.SessionID)
	if loader, ok := scope.ui.(selectionLoader); ok {
		if storedNode, storedActive, err := loader.LoadNodeSelection(ctx); err == nil {
			if storedNode != "" {
				selected = storedNode
			}
			for nodeID, sessionID := range storedActive {
				active[nodeID] = sessionID
			}
		}
	}
	if loader, ok := scope.ui.(historyLoader); ok {
		if stored, err := loader.LoadNodeSessionHistory(ctx); err == nil {
			for nodeID, sessions := range stored {
				recent[nodeID] = append([]domain.SessionID(nil), sessions...)
			}
		}
	}
	if !scope.Available(ctx, selected) {
		selected = scope.local
	}
	if len(active) == 0 {
		if loader, ok := scope.ui.(legacyActiveLoader); ok {
			if sessionID, err := loader.LoadActiveSession(ctx); err == nil && sessionID != "" {
				if session, loadErr := scope.sessions.Load(ctx, sessionID); loadErr == nil {
					active[session.ComputerID()] = sessionID
				}
			}
		}
	}
	current := active[selected]
	if !scope.valid(ctx, selected, current) {
		current = scope.firstValid(ctx, selected, recent[selected])
		if current == "" {
			current = scope.latest(ctx, selected)
		}
		if current == "" {
			delete(active, selected)
		} else {
			active[selected] = current
		}
	}
	scope.mu.Lock()
	scope.selected, scope.active, scope.recent = selected, active, recent
	scope.mu.Unlock()
	if store, ok := scope.ui.(selectionStore); ok {
		if current != "" {
			return current, store.SetNodeActiveSession(ctx, selected, current)
		}
		return current, store.ClearNodeActiveSession(ctx, selected)
	}
	return current, nil
}

func (scope *Scope) Inventory(ctx context.Context) ([]sessioncreation.Computer, error) {
	if scope.env == nil {
		return []sessioncreation.Computer{{ID: scope.local, Name: string(scope.local), Coordinator: true, Available: true}}, nil
	}
	available, err := scope.env.AvailableComputers(ctx)
	if err != nil {
		// A transient capability/provider probe must not make the read-only
		// Status/Nodes surfaces disappear. Keep the coordinator visible and
		// let the next explicit refresh retry discovery.
		return []sessioncreation.Computer{{ID: scope.local, Name: string(scope.local), Coordinator: true, Available: true}}, nil
	}
	live := make(map[domain.ComputerID]sessioncreation.Computer, len(available))
	for _, computer := range available {
		computer.Available = true
		live[computer.ID] = computer
	}
	registered := available
	if inventory, ok := scope.env.(sessioncreation.Inventory); ok {
		registered, err = inventory.RegisteredComputers(ctx)
		if err != nil {
			registered = available
		}
	}
	byID := make(map[domain.ComputerID]sessioncreation.Computer, len(registered)+1)
	for _, computer := range registered {
		if current, ok := live[computer.ID]; ok {
			computer.Available = true
			computer.Capabilities = append([]sessioncreation.ProviderCapability(nil), current.Capabilities...)
			if strings.TrimSpace(current.Name) != "" {
				computer.Name = current.Name
			}
		} else {
			computer.Available = false
		}
		computer.Coordinator = computer.ID == scope.local
		if strings.TrimSpace(computer.Name) == "" {
			computer.Name = string(computer.ID)
		}
		byID[computer.ID] = computer
	}
	for nodeID, computer := range live {
		if _, ok := byID[nodeID]; ok {
			continue
		}
		computer.Coordinator = nodeID == scope.local
		if strings.TrimSpace(computer.Name) == "" {
			computer.Name = string(nodeID)
		}
		byID[nodeID] = computer
	}
	if _, ok := byID[scope.local]; !ok {
		byID[scope.local] = sessioncreation.Computer{ID: scope.local, Name: string(scope.local), Coordinator: true, Available: true}
	}
	nodes := make([]sessioncreation.Computer, 0, len(byID))
	for _, computer := range byID {
		nodes = append(nodes, computer)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Coordinator != nodes[j].Coordinator {
			return nodes[i].Coordinator
		}
		left, right := strings.ToLower(nodes[i].Name), strings.ToLower(nodes[j].Name)
		if left != right {
			return left < right
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes, nil
}

func (scope *Scope) Available(ctx context.Context, nodeID domain.ComputerID) bool {
	if scope.env == nil {
		return nodeID == scope.local
	}
	computers, err := scope.env.AvailableComputers(ctx)
	if err != nil {
		return nodeID == scope.local
	}
	for _, computer := range computers {
		if computer.ID == nodeID {
			return true
		}
	}
	return false
}

func (scope *Scope) Current() domain.ComputerID {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.selected
}

func (scope *Scope) Active(nodeID domain.ComputerID) domain.SessionID {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.active[nodeID]
}

func (scope *Scope) EnsureActive(ctx context.Context, nodeID domain.ComputerID) (domain.SessionID, error) {
	scope.mu.Lock()
	sessionID := scope.active[nodeID]
	history := append([]domain.SessionID(nil), scope.recent[nodeID]...)
	scope.mu.Unlock()
	if scope.valid(ctx, nodeID, sessionID) {
		return sessionID, nil
	}
	sessionID = scope.firstValid(ctx, nodeID, history)
	if sessionID == "" {
		sessionID = scope.latest(ctx, nodeID)
	}
	return sessionID, scope.RestoreActive(ctx, nodeID, sessionID)
}

func (scope *Scope) Select(ctx context.Context, nodeID domain.ComputerID) (domain.SessionID, bool, error) {
	if !scope.Available(ctx, nodeID) {
		return "", false, nil
	}
	scope.mu.Lock()
	current, sessionID, history := scope.selected, scope.active[nodeID], append([]domain.SessionID(nil), scope.recent[nodeID]...)
	scope.mu.Unlock()
	if current == nodeID {
		return sessionID, true, nil
	}
	if !scope.valid(ctx, nodeID, sessionID) {
		sessionID = scope.firstValid(ctx, nodeID, history)
		if sessionID == "" {
			sessionID = scope.latest(ctx, nodeID)
		}
	}
	scope.mu.Lock()
	scope.selected = nodeID
	if sessionID == "" {
		delete(scope.active, nodeID)
	} else {
		scope.active[nodeID] = sessionID
	}
	scope.mu.Unlock()
	if store, ok := scope.ui.(selectionStore); ok {
		if sessionID != "" {
			return sessionID, true, store.SetNodeActiveSession(ctx, nodeID, sessionID)
		}
		return sessionID, true, store.SetSelectedNode(ctx, nodeID)
	}
	return sessionID, true, nil
}

func (scope *Scope) SetActive(ctx context.Context, session domain.Session) error {
	if session.ID() == "" || session.ComputerID() == "" {
		return errors.New("active node session identity is required")
	}
	scope.mu.Lock()
	scope.selected = session.ComputerID()
	scope.active[session.ComputerID()] = session.ID()
	scope.recent[session.ComputerID()] = promote(scope.recent[session.ComputerID()], session.ID())
	scope.mu.Unlock()
	if store, ok := scope.ui.(selectionStore); ok {
		return store.SetNodeActiveSession(ctx, session.ComputerID(), session.ID())
	}
	if store, ok := scope.ui.(legacyActiveStore); ok {
		return store.SetActiveSession(ctx, session.ID())
	}
	return nil
}

func (scope *Scope) RestoreActive(ctx context.Context, nodeID domain.ComputerID, sessionID domain.SessionID) error {
	scope.mu.Lock()
	if sessionID == "" {
		delete(scope.active, nodeID)
	} else {
		scope.active[nodeID] = sessionID
		scope.recent[nodeID] = promote(scope.recent[nodeID], sessionID)
	}
	scope.mu.Unlock()
	if store, ok := scope.ui.(selectionStore); ok {
		if sessionID == "" {
			return store.ClearNodeActiveSession(ctx, nodeID)
		}
		return store.SetNodeActiveSession(ctx, nodeID, sessionID)
	}
	return nil
}

func (scope *Scope) Remove(ctx context.Context, session domain.Session) (domain.SessionID, error) {
	result, err := scope.RemoveClosed(ctx, session)
	return result.Active, err
}

func (scope *Scope) valid(ctx context.Context, nodeID domain.ComputerID, sessionID domain.SessionID) bool {
	if sessionID == "" {
		return false
	}
	session, err := scope.sessions.Load(ctx, sessionID)
	return err == nil && session.ComputerID() == nodeID && (selectableStatus(session.Status()) || session.Status() == domain.SessionAwaitingRecovery)
}

func (scope *Scope) firstValid(ctx context.Context, nodeID domain.ComputerID, history []domain.SessionID) domain.SessionID {
	for _, sessionID := range history {
		if scope.valid(ctx, nodeID, sessionID) {
			return sessionID
		}
	}
	return ""
}

func (scope *Scope) latest(ctx context.Context, nodeID domain.ComputerID) domain.SessionID {
	sessions, err := scope.sessions.List(ctx)
	if err != nil {
		return ""
	}
	var latest domain.Session
	for _, session := range sessions {
		if session.ComputerID() == nodeID && selectableStatus(session.Status()) &&
			(latest.ID() == "" || session.StateChangedAt().After(latest.StateChangedAt())) {
			latest = session
		}
	}
	return latest.ID()
}

func selectableStatus(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionReady, domain.SessionRunning, domain.SessionStopping:
		return true
	default:
		return false
	}
}

func promote(history []domain.SessionID, sessionID domain.SessionID) []domain.SessionID {
	return append([]domain.SessionID{sessionID}, remove(history, sessionID)...)
}

func remove(history []domain.SessionID, sessionID domain.SessionID) []domain.SessionID {
	result := make([]domain.SessionID, 0, len(history))
	for _, candidate := range history {
		if candidate != sessionID {
			result = append(result, candidate)
		}
	}
	return result
}
