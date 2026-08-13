package clusterupdate

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type coordinatorConsensus struct {
	machine *clusterstate.Machine
	leader  string
	local   string
}

func (c *coordinatorConsensus) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	return c.machine.Apply(command), nil
}
func (c *coordinatorConsensus) IsLeader() bool   { return c.local == c.leader }
func (c *coordinatorConsensus) LeaderID() string { return c.leader }
func (c *coordinatorConsensus) TransferLeadershipTo(id string) error {
	c.leader = id
	return nil
}

type coordinatorNodes struct {
	manifest VerifiedManifest
	started  []string
	offline  string
}

func (n *coordinatorNodes) Inspect(context.Context) (VerifiedManifest, error) { return n.manifest, nil }
func (n *coordinatorNodes) Start(_ context.Context, request Request) (Status, error) {
	n.started = append(n.started, request.NodeID)
	return Status{NodeID: request.NodeID, UpdateID: request.UpdateID, Phase: PhaseDownloading}, nil
}
func (n *coordinatorNodes) Status(_ context.Context, request Request) (Status, error) {
	if request.NodeID == n.offline {
		return Status{}, errors.New("unreachable")
	}
	return Status{NodeID: request.NodeID, UpdateID: request.UpdateID, Phase: PhaseDownloading}, nil
}

func TestCoordinatorPreflightDoesNotCommitWhenUpdaterIsUnavailable(t *testing.T) {
	state := domain.NewState()
	for index, id := range []domain.NodeID{"leader", "old-node"} {
		if err := state.AddNode(domain.Node{
			ID: id, Name: string(id), Status: domain.NodeOnline, Lifecycle: domain.NodeActive,
			Version: "v1", OS: "linux", Arch: "amd64", CreatedAt: time.Unix(int64(index+1), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	machine := clusterstate.NewMachine(state)
	consensus := &coordinatorConsensus{machine: machine, leader: "leader", local: "leader"}
	nodes := &coordinatorNodes{offline: "old-node", manifest: VerifiedManifest{
		Manifest: Manifest{Version: "v2", Artifacts: []Artifact{{OS: "linux", Arch: "amd64"}}},
		SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	coordinator, err := NewCoordinator("leader", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background()); err == nil {
		t.Fatal("update started despite an unavailable updater endpoint")
	}
	if machine.State().ClusterUpdate != nil {
		t.Fatal("failed preflight committed a cluster update")
	}
}

func TestCoordinatorDoesNotReinstallCurrentRelease(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "leader", Name: "leader", Status: domain.NodeOnline, Lifecycle: domain.NodeActive,
		Version: "v2", OS: "linux", Arch: "amd64", CreatedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	consensus := &coordinatorConsensus{machine: machine, leader: "leader", local: "leader"}
	nodes := &coordinatorNodes{manifest: VerifiedManifest{
		Manifest: Manifest{Version: "v2", Artifacts: []Artifact{{OS: "linux", Arch: "amd64"}}},
		SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	coordinator, err := NewCoordinator("leader", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background()); err == nil {
		t.Fatal("current release was scheduled again")
	}
	if machine.State().ClusterUpdate != nil || len(nodes.started) != 0 {
		t.Fatal("no-op update changed cluster state")
	}
}

func TestCoordinatorUpdatesFollowersThenTransfersLeadership(t *testing.T) {
	state := domain.NewState()
	for index, id := range []domain.NodeID{"leader", "follower-a", "follower-b"} {
		if err := state.AddNode(domain.Node{
			ID: id, Name: string(id), Status: domain.NodeOnline, Lifecycle: domain.NodeActive,
			Version: "v1", OS: "linux", Arch: "amd64", CreatedAt: time.Unix(int64(index+1), 0),
		}); err != nil {
			t.Fatal(err)
		}
	}
	machine := clusterstate.NewMachine(state)
	consensus := &coordinatorConsensus{machine: machine, leader: "leader", local: "leader"}
	nodes := &coordinatorNodes{manifest: VerifiedManifest{
		Manifest: Manifest{Version: "v2", Artifacts: []Artifact{{OS: "linux", Arch: "amd64"}}},
		SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	coordinator, err := NewCoordinator("leader", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.now = func() time.Time { return time.Unix(100, 0) }
	update, err := coordinator.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.NodeID{"follower-a", "follower-b", "leader"}
	for index := range want {
		if update.Order[index] != want[index] {
			t.Fatalf("order = %v", update.Order)
		}
	}
	ctx := context.Background()
	for _, id := range want[:2] {
		coordinator.reconcile(ctx)
		publishVersion(t, machine, id, "v2")
		coordinator.reconcile(ctx)
	}
	coordinator.reconcile(ctx)
	if consensus.leader != "follower-a" {
		t.Fatalf("leadership was not transferred: %s", consensus.leader)
	}
	consensus.local = "follower-a"
	coordinator, err = NewCoordinator("follower-a", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.now = func() time.Time { return time.Unix(101, 0) }
	coordinator.reconcile(ctx)
	if nodes.started[len(nodes.started)-1] != "leader" {
		t.Fatalf("former leader was not updated last: %v", nodes.started)
	}
	publishVersion(t, machine, "leader", "v2")
	coordinator.reconcile(ctx)
	coordinator.reconcile(ctx)
	if current := machine.State().ClusterUpdate; current == nil || current.Phase != domain.ClusterUpdateCompleted {
		t.Fatalf("update did not complete: %#v", current)
	}
}

func publishVersion(t *testing.T, machine *clusterstate.Machine, nodeID domain.NodeID, version string) {
	t.Helper()
	command, err := clusterstate.NewCommand(
		"heartbeat-"+string(nodeID)+version, clusterstate.CommandUpdateNodeRuntime, time.Now(),
		clusterstate.UpdateNodeRuntime{NodeID: nodeID, Status: domain.NodeOnline, Version: version},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := machine.Apply(command).Err(); err != nil {
		t.Fatal(err)
	}
}
