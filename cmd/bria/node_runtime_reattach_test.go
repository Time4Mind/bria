package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func TestRuntimeReconcilerReattachesOnlyExistingResumeFailedRuntime(t *testing.T) {
	for _, test := range []struct {
		name   string
		exists bool
		live   bool
	}{
		{name: "exact target exists", exists: true, live: true},
		{name: "target absent", exists: false, live: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			machine := runtimeReconcileMachine(t, domain.RuntimeIdle)
			state := machine.State()
			ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
			session := state.Sessions[ref.Key()]
			if err := state.ArchiveSession(
				ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(150, 0).UTC(),
			); err != nil {
				t.Fatal(err)
			}
			machine = clusterstate.NewMachine(state)
			existence := &runtimeExistenceStub{exists: test.exists}
			runtimes := &runtimeRegistryStub{}
			reconciler, err := newRuntimeMissingReconciler(config.Config{
				NodeID: "node", TmuxSession: "bria",
			}, machine, existence, machineApplier{machine}, runtimes)
			if err != nil {
				t.Fatal(err)
			}
			reconciler.now = func() time.Time { return time.Unix(200, 0).UTC() }
			reconciler.newID = func() (string, error) { return "reattach", nil }

			if err := reconciler.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := machine.State().Sessions[ref.Key()]
			if got.IsLive() != test.live {
				t.Fatalf("reattached session=%#v", got)
			}
			if !test.live {
				if got.State != domain.SessionArchived || runtimes.registerCalls != 0 {
					t.Fatalf("absent runtime changed session=%#v registry=%#v", got, runtimes)
				}
				return
			}
			if got.RuntimePhase != domain.RuntimeIdle || got.ArchiveReason != "" ||
				got.RestoredAt != time.Unix(200, 0).UTC() {
				t.Fatalf("reattached state=%#v", got)
			}
			if runtimes.registerCalls != 1 ||
				runtimes.binding.TmuxTarget != runtimehost.TmuxTarget("bria", "node", "session") ||
				runtimes.binding.Generation != 4 {
				t.Fatalf("reattached binding=%#v calls=%d", runtimes.binding, runtimes.registerCalls)
			}
		})
	}
}

func TestRuntimeReconcilerRetriesRegistrationAfterDurableReattach(t *testing.T) {
	machine := runtimeReconcileMachine(t, domain.RuntimeIdle)
	state := machine.State()
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	session := state.Sessions[ref.Key()]
	if err := state.ArchiveSession(
		ref, session.Revision, domain.ArchiveResumeFailed, time.Unix(150, 0).UTC(),
	); err != nil {
		t.Fatal(err)
	}
	machine = clusterstate.NewMachine(state)
	runtimes := &runtimeRegistryStub{registerErr: errors.New("temporary registry failure")}
	reconciler, err := newRuntimeMissingReconciler(config.Config{
		NodeID: "node", TmuxSession: "bria",
	}, machine, &runtimeExistenceStub{exists: true}, machineApplier{machine}, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.newID = func() (string, error) { return "reattach-retry", nil }
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("registry failure was not surfaced")
	}
	if !machine.State().Sessions[ref.Key()].IsLive() || runtimes.registerCalls != 1 {
		t.Fatalf("durable reattach did not precede registration: %#v", runtimes)
	}
	runtimes.registerErr = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtimes.registerCalls != 2 {
		t.Fatalf("registration attempts=%d", runtimes.registerCalls)
	}
}
