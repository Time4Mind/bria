package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type runtimeExistenceStub struct {
	exists bool
	err    error
	calls  int
}

func (s *runtimeExistenceStub) TargetExists(context.Context, string) (bool, error) {
	s.calls++
	return s.exists, s.err
}

type runtimeRegistryStub struct {
	registerCalls   int
	binding         runtimehost.RuntimeBinding
	registerErr     error
	unregisterCalls int
	nodeID          string
	sessionID       string
	generation      uint64
	err             error
}

type machineApplier struct{ machine *clusterstate.Machine }

func (a machineApplier) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	return a.machine.Apply(command), nil
}

type mutateBeforeApply struct {
	machine *clusterstate.Machine
	mutate  clusterstate.Command
}

func (a mutateBeforeApply) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	if result := a.machine.Apply(a.mutate); result.Err() != nil {
		return clusterstate.Result{}, result.Err()
	}
	return a.machine.Apply(command), nil
}

func (s *runtimeRegistryStub) Register(binding runtimehost.RuntimeBinding) error {
	s.registerCalls++
	s.binding = binding
	return s.registerErr
}

func (s *runtimeRegistryStub) Unregister(nodeID, sessionID string, generation uint64) error {
	s.unregisterCalls++
	s.nodeID, s.sessionID, s.generation = nodeID, sessionID, generation
	return s.err
}

func TestRuntimeReconcilerArchivesMissingSessionAfterConsecutiveChecks(t *testing.T) {
	machine := runtimeReconcileMachine(t, domain.RuntimeRunning)
	existence := &runtimeExistenceStub{}
	runtimes := &runtimeRegistryStub{}
	reconciler, err := newRuntimeMissingReconciler(config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, machine, existence, machineApplier{machine}, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0).UTC()
	reconciler.now = func() time.Time { return now }
	reconciler.newID = func() (string, error) { return "operation", nil }

	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !machine.State().Sessions["node/session"].IsLive() || runtimes.unregisterCalls != 0 {
		t.Fatal("single missing observation changed session")
	}
	existence.exists = true
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	existence.exists = false
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	session := machine.State().Sessions["node/session"]
	if session.State != domain.SessionArchived ||
		session.ArchiveID != clusterstate.MissingArchiveID("operation") ||
		session.ArchiveReason != domain.ArchiveResumeFailed || session.ArchiveReady ||
		session.LastEventAt != now {
		t.Fatalf("archived session=%#v", session)
	}
	if runtimes.unregisterCalls != 1 || runtimes.nodeID != "node" ||
		runtimes.sessionID != "session" || runtimes.generation != 4 {
		t.Fatalf("runtimes=%#v", runtimes)
	}
	if machine.State().IsSessionLost(session.Ref()) {
		t.Fatal("missing local runtime was incorrectly classified as Lost")
	}
}

func TestRuntimeReconcilerIgnoresProbeErrorsAndPendingRuntime(t *testing.T) {
	for _, phase := range []domain.RuntimePhase{domain.RuntimeStarting, domain.RuntimeDegraded} {
		machine := runtimeReconcileMachine(t, phase)
		state := machine.State()
		session := state.Sessions["node/session"]
		if phase == domain.RuntimeDegraded {
			session.ResumePending = true
			state.Sessions[session.Ref().Key()] = session
			machine = clusterstate.NewMachine(state)
		}
		existence := &runtimeExistenceStub{err: errors.New("tmux unavailable")}
		reconciler, err := newRuntimeMissingReconciler(config.Config{
			NodeID: "node", TmuxSession: "bria",
		}, machine, existence, machineApplier{machine}, &runtimeRegistryStub{})
		if err != nil {
			t.Fatal(err)
		}
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if existence.calls != 0 || !machine.State().Sessions["node/session"].IsLive() {
			t.Fatalf("pending phase %s was probed or changed", phase)
		}
	}

	machine := runtimeReconcileMachine(t, domain.RuntimeIdle)
	existence := &runtimeExistenceStub{err: errors.New("tmux unavailable")}
	reconciler, _ := newRuntimeMissingReconciler(config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, machine, existence, machineApplier{machine}, &runtimeRegistryStub{})
	for range runtimeMissingThreshold + 1 {
		if err := reconciler.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !machine.State().Sessions["node/session"].IsLive() {
		t.Fatal("tmux inspection errors were counted as missing")
	}
}

func TestRuntimeReconcilerCannotArchiveNewerSessionRevision(t *testing.T) {
	machine := runtimeReconcileMachine(t, domain.RuntimeIdle)
	activity, err := clusterstate.NewCommand(
		"runtime", clusterstate.CommandPublishSessionRuntime, time.Unix(150, 0).UTC(),
		clusterstate.PublishSessionRuntime{
			Session:    domain.SessionRef{NodeID: "node", SessionID: "session"},
			Generation: 4, Phase: domain.RuntimeRunning,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimes := &runtimeRegistryStub{}
	reconciler, err := newRuntimeMissingReconciler(config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, machine, &runtimeExistenceStub{}, mutateBeforeApply{
		machine: machine, mutate: activity,
	}, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.newID = func() (string, error) { return "stale", nil }
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err == nil ||
		!strings.Contains(err.Error(), domain.ErrStaleOperation.Error()) {
		t.Fatalf("stale reconcile error=%v", err)
	}
	if !machine.State().Sessions["node/session"].IsLive() || runtimes.unregisterCalls != 0 {
		t.Fatal("stale reconcile archived or unregistered newer session")
	}
}

func TestRuntimeReconcilerRegistersExistingSessionWindow(t *testing.T) {
	machine := runtimeReconcileMachine(t, domain.RuntimeDegraded)
	runtimes := &runtimeRegistryStub{}
	reconciler, err := newRuntimeMissingReconciler(config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, machine, &runtimeExistenceStub{exists: true}, machineApplier{machine}, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtimes.registerCalls != 1 {
		t.Fatalf("register calls=%d", runtimes.registerCalls)
	}
	if runtimes.binding.NodeID != "node" || runtimes.binding.SessionID != "session" ||
		runtimes.binding.Generation != 4 ||
		runtimes.binding.TmuxTarget != runtimehost.TmuxTarget("bria", "node", "session") ||
		runtimes.binding.Backend != "codex" || runtimes.binding.Workdir != "/workspace" {
		t.Fatalf("binding=%#v", runtimes.binding)
	}
}

func runtimeReconcileMachine(t *testing.T, phase domain.RuntimePhase) *clusterstate.Machine {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "node", Name: "Node", Status: domain.NodeOnline,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetSoleOwner(7); err != nil {
		t.Fatal(err)
	}
	created := time.Unix(100, 0).UTC()
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Session",
		Workdir: "/workspace", Backend: "codex", ProviderSessionID: "provider",
		State: domain.SessionLive, RuntimePhase: phase, RuntimeGeneration: 4,
		CreatedAt: created, LiveSinceAt: created, LastEventAt: created,
	}); err != nil {
		t.Fatal(err)
	}
	return clusterstate.NewMachine(state)
}

var _ runtimeRegistry = (*runtimeRegistryStub)(nil)
var _ runtimeExistence = (*runtimeExistenceStub)(nil)
