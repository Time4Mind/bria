package sessioncontrol

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func controllerFixture(t *testing.T) (*Controller, *runtimeStub, *clusterstate.Machine) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Named", Backend: "claude",
		ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 1,
		CreatedAt: time.Unix(1, 0), LiveSinceAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	port := machinePort{machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeStub{results: make(map[string]runtimehost.Result)}
	controller, err := New(service, runtime)
	if err != nil {
		t.Fatal(err)
	}
	controller.pollInterval = time.Millisecond
	controller.retryInterval = time.Millisecond
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := controller.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return controller, runtime, machine
}

func waitForPhase(
	t *testing.T,
	machine *clusterstate.Machine,
	ref domain.SessionRef,
	want domain.RuntimePhase,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if machine.State().Sessions[ref.Key()].RuntimePhase == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("phase did not become %q", want)
}
