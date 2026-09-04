package telegramnodes_test

import (
	"context"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramnodes"
)

type sessionStore struct {
	sessions map[domain.SessionID]domain.Session
}

func (store *sessionStore) List(context.Context) ([]domain.Session, error) {
	result := make([]domain.Session, 0, len(store.sessions))
	for _, session := range store.sessions {
		result = append(result, session)
	}
	return result, nil
}

func (store *sessionStore) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	return store.sessions[id], nil
}

type uiState struct {
	selected domain.ComputerID
	active   map[domain.ComputerID]domain.SessionID
	history  map[domain.ComputerID][]domain.SessionID
}

func (state *uiState) LoadNodeSelection(context.Context) (domain.ComputerID, map[domain.ComputerID]domain.SessionID, error) {
	return state.selected, state.active, nil
}
func (state *uiState) LoadNodeSessionHistory(context.Context) (map[domain.ComputerID][]domain.SessionID, error) {
	return state.history, nil
}
func (state *uiState) SetSelectedNode(_ context.Context, id domain.ComputerID) error {
	state.selected = id
	return nil
}
func (state *uiState) SetNodeActiveSession(_ context.Context, node domain.ComputerID, id domain.SessionID) error {
	state.selected, state.active[node] = node, id
	state.history[node] = promote(state.history[node], id)
	return nil
}
func (state *uiState) ClearNodeActiveSession(_ context.Context, node domain.ComputerID) error {
	delete(state.active, node)
	return nil
}

func TestRemovedActiveFallsBackToPreviousMostRecentlyActive(t *testing.T) {
	first := ready(t, "11111111-1111-4111-9111-111111111111")
	second := ready(t, "22222222-2222-4222-9222-222222222222")
	store := &sessionStore{sessions: map[domain.SessionID]domain.Session{first.ID(): first, second.ID(): second}}
	ui := &uiState{active: map[domain.ComputerID]domain.SessionID{}, history: map[domain.ComputerID][]domain.SessionID{}}
	scope, err := telegramnodes.New("local", store, ui, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := scope.SetActive(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := scope.SetActive(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	archived := archive(t, second)
	store.sessions[second.ID()] = archived
	fallback, err := scope.Remove(context.Background(), archived)
	if err != nil || fallback != first.ID() || ui.active["local"] != first.ID() {
		t.Fatalf("fallback=%q persisted=%q err=%v", fallback, ui.active["local"], err)
	}
}

func ready(t *testing.T, id domain.SessionID) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSession(id, domain.IntentID("intent-"+id), "local", domain.ProviderCodex, "/work")
	if err != nil {
		t.Fatal(err)
	}
	result, err := starting.Ready(domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-" + string(id), Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func archive(t *testing.T, session domain.Session) domain.Session {
	t.Helper()
	closing, err := session.BeginClose(session.StateChangedAt().Add(1))
	if err != nil {
		t.Fatal(err)
	}
	result, err := closing.Archive(closing.StateChangedAt().Add(1))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func promote(history []domain.SessionID, sessionID domain.SessionID) []domain.SessionID {
	result := []domain.SessionID{sessionID}
	for _, candidate := range history {
		if candidate != sessionID {
			result = append(result, candidate)
		}
	}
	return result
}
