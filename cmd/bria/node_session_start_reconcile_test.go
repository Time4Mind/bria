package main

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessionstart"
)

type localStartStateStub struct{ state *domain.State }

func (s localStartStateStub) State() *domain.State { return s.state.Clone() }

type localProvisionerStub struct {
	requests []sessionstart.ProvisionRequest
	err      error
}

func (s *localProvisionerStub) Provision(
	_ context.Context,
	request sessionstart.ProvisionRequest,
) error {
	s.requests = append(s.requests, request)
	return s.err
}

type localUnregisterStub struct {
	calls int
	err   error
}

func (s *localUnregisterStub) Unregister(string, string, uint64) error {
	s.calls++
	return s.err
}

func TestLocalSessionStartReconcilerProvisionsOnlyOwnedStartingSessions(t *testing.T) {
	state := domain.NewState()
	now := time.Now().UTC()
	for _, nodeID := range []domain.NodeID{"node", "other"} {
		if err := state.AddNode(domain.Node{ID: nodeID, Name: string(nodeID), Status: domain.NodeOnline}); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node", "other"); err != nil {
		t.Fatal(err)
	}
	for _, session := range []domain.Session{
		{ID: "local", NodeID: "node", OwnerID: 7, Name: "", Backend: "codex",
			Workdir: "/workspace", State: domain.SessionLive,
			RuntimePhase: domain.RuntimeStarting, RuntimeGeneration: 1,
			CreatedAt: now, LiveSinceAt: now, LastEventAt: now},
		{ID: "ready", NodeID: "node", OwnerID: 7, Name: "ready", Backend: "codex",
			Workdir: "/workspace", State: domain.SessionLive,
			RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 1,
			CreatedAt: now, LiveSinceAt: now, LastEventAt: now},
		{ID: "remote", NodeID: "other", OwnerID: 7, Name: "remote", Backend: "codex",
			Workdir: "/workspace", State: domain.SessionLive,
			RuntimePhase: domain.RuntimeStarting, RuntimeGeneration: 1,
			CreatedAt: now, LiveSinceAt: now, LastEventAt: now},
	} {
		if err := state.AddSession(session); err != nil {
			t.Fatal(err)
		}
	}
	provisioner := &localProvisionerStub{}
	reconciler := &localSessionStartReconciler{
		nodeID: "node", state: localStartStateStub{state}, provisioner: provisioner,
		runtimes: &localUnregisterStub{}, tracked: make(map[string]domain.Session),
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(provisioner.requests) != 1 || provisioner.requests[0].Session.SessionID != "local" {
		t.Fatalf("provisioned=%#v", provisioner.requests)
	}
}

func TestLocalSessionStartReconcilerDrainsFailedStartAfterArchive(t *testing.T) {
	state := domain.NewState()
	now := time.Now().UTC()
	if err := state.AddNode(domain.Node{ID: "node", Name: "node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "local", NodeID: "node", OwnerID: 7, Backend: "codex", Workdir: "/workspace",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting, RuntimeGeneration: 2,
		CreatedAt: now, LiveSinceAt: now, LastEventAt: now,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	unregister := &localUnregisterStub{err: runtimehost.ErrRuntimeUnavailable}
	reconciler := &localSessionStartReconciler{
		nodeID: "node", state: localStartStateStub{state}, provisioner: &localProvisionerStub{},
		runtimes: unregister, tracked: make(map[string]domain.Session),
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	archived := state.Sessions[session.Ref().Key()]
	archived.State = domain.SessionArchived
	state.Sessions[session.Ref().Key()] = archived
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unregister.calls != 1 {
		t.Fatalf("unregister calls=%d", unregister.calls)
	}
}
