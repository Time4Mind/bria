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
		nodeID: "node", tmuxSession: "bria",
		state: localStartStateStub{state}, provisioner: provisioner,
		runtimes: &localUnregisterStub{}, targets: &runtimeExistenceStub{},
		tracked: make(map[string]domain.Session), terminal: make(map[string]terminalStartRuntime),
		now: time.Now,
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
	targets := &runtimeExistenceStub{exists: true}
	clock := now
	reconciler := &localSessionStartReconciler{
		nodeID: "node", tmuxSession: "bria",
		state: localStartStateStub{state}, provisioner: &localProvisionerStub{},
		runtimes: unregister, targets: targets,
		tracked: make(map[string]domain.Session), terminal: make(map[string]terminalStartRuntime),
		now: func() time.Time { return clock },
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
	if unregister.calls != 0 || targets.closeCalls != 0 {
		t.Fatal("terminal runtime was cleaned without provisioning grace")
	}
	clock = clock.Add(localSessionStartCleanupGrace + time.Second)
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if targets.closeCalls != 0 {
		t.Fatal("terminal runtime was closed after only one confirmation")
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unregister.calls != 1 || targets.closeCalls != 1 || targets.exists {
		t.Fatalf("cleanup unregister=%d close=%d exists=%t", unregister.calls, targets.closeCalls, targets.exists)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if unregister.calls != 1 || targets.closeCalls != 1 {
		t.Fatal("terminal generation cleanup repeated after completion")
	}
}

func TestLocalSessionStartReconcilerNeverCleansSlowStartingSession(t *testing.T) {
	state := domain.NewState()
	now := time.Unix(100, 0).UTC()
	if err := state.AddNode(domain.Node{ID: "node", Name: "node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "slow", NodeID: "node", OwnerID: 7, Backend: "codex", Workdir: "/workspace",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting, RuntimeGeneration: 3,
		CreatedAt: now, LiveSinceAt: now, LastEventAt: now,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	targets := &runtimeExistenceStub{exists: true}
	unregister := &localUnregisterStub{}
	clock := now
	reconciler := &localSessionStartReconciler{
		nodeID: "node", tmuxSession: "bria", state: localStartStateStub{state},
		provisioner: &localProvisionerStub{}, runtimes: unregister, targets: targets,
		tracked: make(map[string]domain.Session), terminal: make(map[string]terminalStartRuntime),
		now: func() time.Time { return clock },
	}
	for range 3 {
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(localSessionStartCleanupGrace)
	}
	if targets.closeCalls != 0 || unregister.calls != 0 || len(reconciler.terminal) != 0 {
		t.Fatal("slow RuntimeStarting session was scheduled for cleanup")
	}
}

func TestLocalSessionStartReconcilerRechecksNewGenerationBeforeClose(t *testing.T) {
	state := domain.NewState()
	now := time.Unix(100, 0).UTC()
	if err := state.AddNode(domain.Node{ID: "node", Name: "node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	session := domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Backend: "codex", Workdir: "/workspace",
		State: domain.SessionLive, RuntimePhase: domain.RuntimeStarting, RuntimeGeneration: 1,
		CreatedAt: now, LiveSinceAt: now, LastEventAt: now,
	}
	if err := state.AddSession(session); err != nil {
		t.Fatal(err)
	}
	targets := &runtimeExistenceStub{exists: true}
	unregister := &localUnregisterStub{}
	clock := now
	reconciler := &localSessionStartReconciler{
		nodeID: "node", tmuxSession: "bria", state: localStartStateStub{state},
		provisioner: &localProvisionerStub{}, runtimes: unregister, targets: targets,
		tracked: make(map[string]domain.Session), terminal: make(map[string]terminalStartRuntime),
		now: func() time.Time { return clock },
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
	newer := archived
	newer.State = domain.SessionLive
	newer.RuntimePhase = domain.RuntimeStarting
	newer.RuntimeGeneration = 2
	state.Sessions[session.Ref().Key()] = newer
	clock = clock.Add(localSessionStartCleanupGrace + time.Second)
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if targets.closeCalls != 0 || unregister.calls != 0 || len(reconciler.terminal) != 0 {
		t.Fatal("newer live generation was closed by stale cleanup")
	}
}

func TestLocalSessionStartReconcilerRecoversTerminalFreshStartAfterRestart(t *testing.T) {
	for _, test := range []struct {
		name           string
		providerResume bool
		wantClosed     bool
	}{
		{name: "fresh failed start", wantClosed: true},
		{name: "provider resume remains recoverable", providerResume: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := domain.NewState()
			created := time.Unix(100, 0).UTC()
			if err := state.AddNode(domain.Node{ID: "node", Name: "node", Status: domain.NodeOnline}); err != nil {
				t.Fatal(err)
			}
			if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
				t.Fatal(err)
			}
			session := domain.Session{
				ID: "session", NodeID: "node", OwnerID: 7, Backend: "codex",
				Workdir: "/workspace", ProviderSessionID: "provider",
				ProviderResume: test.providerResume,
				State:          domain.SessionLive, RuntimePhase: domain.RuntimeStarting,
				RuntimeGeneration: 1, CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
			}
			if err := state.AddSession(session); err != nil {
				t.Fatal(err)
			}
			archived := state.Sessions[session.Ref().Key()]
			archived.State = domain.SessionArchived
			archived.RuntimePhase = domain.RuntimeIdle
			archived.ArchiveReason = domain.ArchiveResumeFailed
			archived.ArchivedAt = created.Add(time.Minute)
			state.Sessions[session.Ref().Key()] = archived
			targets := &runtimeExistenceStub{exists: true}
			unregister := &localUnregisterStub{}
			reconciler := &localSessionStartReconciler{
				nodeID: "node", tmuxSession: "bria", state: localStartStateStub{state},
				provisioner: &localProvisionerStub{}, runtimes: unregister, targets: targets,
				tracked: make(map[string]domain.Session), terminal: make(map[string]terminalStartRuntime),
				now: func() time.Time { return created.Add(10 * time.Minute) },
			}
			for range localSessionStartConfirmations {
				if err := reconciler.Reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if got := targets.closeCalls > 0; got != test.wantClosed {
				t.Fatalf("closed=%t want=%t", got, test.wantClosed)
			}
			if !test.wantClosed && len(reconciler.terminal) != 0 {
				t.Fatal("recoverable provider resume entered terminal cleanup")
			}
		})
	}
}
