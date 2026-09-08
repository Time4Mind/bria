package telegramnodes_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramnodes"
)

type atomicCloseUI struct {
	*uiState
	fail bool
}

func (s *atomicCloseUI) SetNodeLastActiveSession(_ context.Context, node domain.ComputerID, id domain.SessionID) error {
	if s.fail {
		return errors.New("synthetic persistence rejection")
	}
	s.active[node] = id
	s.history[node] = promote(s.history[node], id)
	return nil
}

func TestRemoveClosedCleansBackgroundNodeWithoutForegroundBounce(t *testing.T) {
	ctx := context.Background()
	first, closing := ready(t, "11111111-1111-4111-9111-111111111111"), ready(t, "22222222-2222-4222-9222-222222222222")
	remoteSnapshot := ready(t, "33333333-3333-4333-9333-333333333333").Snapshot()
	remoteSnapshot.ComputerID = "remote"
	remote, err := domain.RestoreSession(remoteSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	store := &sessionStore{sessions: map[domain.SessionID]domain.Session{first.ID(): first, closing.ID(): closing, remote.ID(): remote}}
	for _, atomic := range []bool{true, false} {
		t.Run(map[bool]string{true: "atomic_persistence", false: "missing_background_port"}[atomic], func(t *testing.T) {
			ui := &uiState{active: map[domain.ComputerID]domain.SessionID{}, history: map[domain.ComputerID][]domain.SessionID{}}
			var port any = ui
			if atomic {
				port = &atomicCloseUI{uiState: ui}
			}
			scope, err := telegramnodes.New("local", store, port, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, session := range []domain.Session{first, closing, remote} {
				if err := scope.SetActive(ctx, session); err != nil {
					t.Fatal(err)
				}
			}
			archived := archive(t, closing)
			store.sessions[closing.ID()] = archived
			result, err := scope.RemoveClosed(ctx, archived)
			if err != nil || result.Active != first.ID() || !result.Recent || result.Persisted != atomic {
				t.Fatalf("background cleanup = %+v, %v", result, err)
			}
			if ui.selected != "remote" || scope.Current() != "remote" || scope.Active("remote") != remote.ID() || ui.active["remote"] != remote.ID() {
				t.Fatalf("foreground changed: %#v", ui)
			}
			if scope.Active("local") != first.ID() {
				t.Fatal("background cached selection still names archived session")
			}
			if atomic && ui.active["local"] != first.ID() {
				t.Fatal("background durable selection still names archived session")
			}
		})
	}
}

func TestRemoveClosedRetriesFailedPersistenceAndDoesNotClaimAbsentStore(t *testing.T) {
	ctx := context.Background()
	first, current := ready(t, "11111111-1111-4111-9111-111111111111"), ready(t, "22222222-2222-4222-9222-222222222222")
	store := &sessionStore{sessions: map[domain.SessionID]domain.Session{first.ID(): first, current.ID(): current}}
	ui := &atomicCloseUI{uiState: &uiState{active: map[domain.ComputerID]domain.SessionID{}, history: map[domain.ComputerID][]domain.SessionID{}}}
	scope, err := telegramnodes.New("local", store, ui, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range []domain.Session{first, current} {
		if err := scope.SetActive(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	archived := archive(t, current)
	store.sessions[current.ID()] = archived
	ui.fail = true
	result, err := scope.RemoveClosed(ctx, archived)
	if err == nil || result.Persisted || scope.Active("local") != current.ID() || ui.active["local"] != current.ID() {
		t.Fatalf("failed write leaked success/cache: %+v, %v", result, err)
	}
	ui.fail = false
	result, err = scope.RemoveClosed(ctx, archived)
	if err != nil || !result.Persisted || scope.Active("local") != first.ID() || ui.active["local"] != first.ID() {
		t.Fatalf("retry failed: %+v, %v", result, err)
	}
	nilScope, err := telegramnodes.New("local", store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = nilScope.RemoveClosed(ctx, archived)
	if err != nil || result.Persisted || result.Active != first.ID() {
		t.Fatalf("absent store claimed persistence: %+v, %v", result, err)
	}
}
