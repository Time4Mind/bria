package providerstop

import (
	"context"
	"testing"
)

type testLeadership string

func (l testLeadership) LeaderID() string { return string(l) }

type recordingRemote struct {
	nodeID string
	signal Signal
}

func (r *recordingRemote) NotifyProviderStop(
	_ context.Context,
	nodeID string,
	signal Signal,
) error {
	r.nodeID, r.signal = nodeID, signal
	return nil
}

func TestRouterDeliversLocallyOnlyOnLeader(t *testing.T) {
	signal := Signal{NodeID: "worker", SessionID: "session", ProviderSessionID: "provider"}
	bus := NewBus(1)
	remote := &recordingRemote{}
	router, err := NewRouter("leader", testLeadership("leader"), bus, remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Notify(context.Background(), signal); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-bus.Events():
		if got != signal {
			t.Fatalf("local signal=%#v", got)
		}
	default:
		t.Fatal("leader did not receive provider stop")
	}
	if remote.nodeID != "" {
		t.Fatalf("leader forwarded signal to %q", remote.nodeID)
	}
}

func TestRouterForwardsToCurrentLeader(t *testing.T) {
	signal := Signal{NodeID: "worker", SessionID: "session", ProviderSessionID: "provider"}
	remote := &recordingRemote{}
	router, err := NewRouter("worker", testLeadership("leader"), NewBus(1), remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Notify(context.Background(), signal); err != nil {
		t.Fatal(err)
	}
	if remote.nodeID != "leader" || remote.signal != signal {
		t.Fatalf("forwarded node=%q signal=%#v", remote.nodeID, remote.signal)
	}
}
