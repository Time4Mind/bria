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

type leadershipStub bool

func (l leadershipStub) IsLeader() bool { return bool(l) }

func TestOfflineInputsPersistAndDrainFIFOAfterRecovery(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	setNodeStatus(t, machine, domain.NodeOffline, "offline")
	actor := application.Principal{UserID: 7}
	for _, item := range []struct{ id, text string }{{"queued-1", "first"}, {"queued-2", "second"}} {
		accepted, err := controller.SendInput(context.Background(), actor, item.id, item.text)
		if err != nil || !accepted.Deferred {
			t.Fatalf("queue %s: accepted=%+v err=%v", item.id, accepted, err)
		}
	}
	if got := len(machine.State().DeferredInputs["node/session"]); got != 2 {
		t.Fatalf("replicated queue length=%d", got)
	}
	runtime.mu.Lock()
	if len(runtime.requests) != 0 {
		t.Fatalf("offline requests reached runtime: %#v", runtime.requests)
	}
	runtime.results["queued-1"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.results["queued-2"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	setNodeStatus(t, machine, domain.NodeOnline, "online")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = controller.RunDeferredInputs(ctx, leadershipStub(true), time.Millisecond) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(machine.State().DeferredInputs["node/session"]) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(machine.State().DeferredInputs["node/session"]); got != 0 {
		t.Fatalf("queue did not drain: %d", got)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.requests) < 2 || runtime.requests[0].Text != "first" || runtime.requests[1].Text != "second" {
		t.Fatalf("runtime order=%#v", runtime.requests)
	}
}

func TestOnlineInputJoinsExistingBacklogWithoutOvertaking(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	setNodeStatus(t, machine, domain.NodeOffline, "offline")
	actor := application.Principal{UserID: 7}
	if _, err := controller.SendInput(context.Background(), actor, "queued-1", "first"); err != nil {
		t.Fatal(err)
	}
	setNodeStatus(t, machine, domain.NodeOnline, "online")
	accepted, err := controller.SendInput(context.Background(), actor, "queued-2", "second")
	if err != nil || !accepted.Deferred {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.requests) != 0 {
		t.Fatalf("new input overtook backlog: %#v", runtime.requests)
	}
}

func setNodeStatus(t *testing.T, machine *clusterstate.Machine, status domain.NodeStatus, operationID string) {
	t.Helper()
	command, err := clusterstate.NewCommand(
		operationID, clusterstate.CommandUpdateNodeRuntime, time.Now(),
		clusterstate.UpdateNodeRuntime{NodeID: "node", Status: status},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
}
