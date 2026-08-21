package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/providerbinding"
)

type bindingGCState struct{ state *domain.State }

func (s bindingGCState) State() *domain.State { return s.state.Clone() }

type bindingGCStore struct {
	records []providerbinding.Record
	input   providerbinding.SweepInput
}

func (s *bindingGCStore) Snapshot() ([]providerbinding.Record, error) {
	return append([]providerbinding.Record(nil), s.records...), nil
}

func (s *bindingGCStore) Sweep(input providerbinding.SweepInput) error {
	s.input = input
	return nil
}

type bindingGCTargets struct {
	exists map[string]bool
	err    error
}

func (p bindingGCTargets) TargetExists(_ context.Context, target string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.exists[target], nil
}

func TestProviderBindingReconcilerKeepsLiveAndDeletesSettledArchive(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	state := domain.NewState()
	live := bindingGCSession("live", domain.SessionLive, false)
	archived := bindingGCSession("archived", domain.SessionArchived, true)
	pending := bindingGCSession("pending", domain.SessionArchived, false)
	for _, session := range []domain.Session{live, archived, pending} {
		state.Sessions[session.Ref().Key()] = session
	}
	store := &bindingGCStore{records: []providerbinding.Record{
		bindingGCRecord(live, now), bindingGCRecord(archived, now),
		bindingGCRecord(pending, now), bindingGCRecord(bindingGCSession("missing", domain.SessionLive, false), now),
	}}
	reconciler, err := newProviderBindingReconciler(
		config.Config{NodeID: "node", TmuxSession: "bria"}, bindingGCState{state}, store,
		bindingGCTargets{exists: map[string]bool{}},
	)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return now }
	if err := reconciler.Reconcile(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(store.input.Archived) != 1 || store.input.Archived[0].Ref != archived.Ref() ||
		!store.input.Archived[0].TargetAbsent {
		t.Fatalf("archived candidates=%#v", store.input.Archived)
	}
	if !bindingGCContains(store.input.KeepRefs, live.Ref()) ||
		!bindingGCContains(store.input.KeepRefs, pending.Ref()) ||
		!bindingGCContains(store.input.KeepRefs, domain.SessionRef{NodeID: "node", SessionID: "missing"}) {
		t.Fatalf("keep refs=%#v", store.input.KeepRefs)
	}
	if !store.input.MissingBefore.IsZero() {
		t.Fatalf("fast pass supplied missing cutoff: %s", store.input.MissingBefore)
	}
}

func TestProviderBindingFullReconcileUsesGraceAndProbeErrorsKeepBinding(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	state := domain.NewState()
	archived := bindingGCSession("archived", domain.SessionArchived, true)
	state.Sessions[archived.Ref().Key()] = archived
	store := &bindingGCStore{records: []providerbinding.Record{
		bindingGCRecord(archived, now),
		bindingGCRecord(bindingGCSession("missing", domain.SessionLive, false), now),
	}}
	reconciler, err := newProviderBindingReconciler(
		config.Config{NodeID: "node", TmuxSession: "bria"}, bindingGCState{state}, store,
		bindingGCTargets{err: errors.New("tmux unavailable")},
	)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return now }
	if err := reconciler.Reconcile(context.Background(), true); err == nil {
		t.Fatal("target probe error was hidden")
	}
	if store.input.MissingBefore != now.Add(-providerBindingMissingGrace) {
		t.Fatalf("missing cutoff=%s", store.input.MissingBefore)
	}
	if !bindingGCContains(store.input.KeepRefs, archived.Ref()) ||
		bindingGCContains(store.input.KeepRefs, domain.SessionRef{NodeID: "node", SessionID: "missing"}) {
		t.Fatalf("keep refs=%#v", store.input.KeepRefs)
	}
}

func bindingGCSession(id string, state domain.SessionState, ready bool) domain.Session {
	return domain.Session{
		ID: domain.SessionID(id), NodeID: "node", OwnerID: 7, State: state,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 2, ArchiveReady: ready,
	}
}

func bindingGCRecord(session domain.Session, at time.Time) providerbinding.Record {
	return providerbinding.Record{
		NodeID: string(session.NodeID), SessionID: string(session.ID),
		ProviderSessionID: "019fffe8-02ee-7aa1-b6cf-eed13a005482",
		Workdir:           "/workspace", TmuxSession: "bria", TmuxWindow: "window",
		RuntimeGeneration: 1, UpdatedAt: at,
	}
}

func bindingGCContains(refs []domain.SessionRef, want domain.SessionRef) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}
