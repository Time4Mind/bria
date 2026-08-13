package consensus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/hashicorp/raft"
)

// Raft transitions normally settle in under a second. Keep enough headroom for
// emulated ARM runners where unrelated package builds can temporarily starve
// the in-memory transport goroutines.
const clusterTestTimeout = 15 * time.Second

type inMemoryCluster struct {
	ids        []string
	nodes      map[string]*Node
	addresses  map[string]raft.ServerAddress
	transports map[string]*raft.InmemTransport
	stopped    map[string]bool
}

func newThreeNodeCluster(t *testing.T) *inMemoryCluster {
	t.Helper()
	cluster := newInMemoryCluster(t, []string{"raft-1", "raft-2", "raft-3"})
	cluster.requireThreeVoters(t, cluster.nodes[cluster.waitForLeader(t)])
	return cluster
}

func newTwoNodeCluster(t *testing.T) *inMemoryCluster {
	t.Helper()
	return newInMemoryCluster(t, []string{"raft-1", "raft-2"})
}

func newInMemoryCluster(t *testing.T, ids []string) *inMemoryCluster {
	t.Helper()

	cluster := &inMemoryCluster{
		ids:        append([]string(nil), ids...),
		nodes:      make(map[string]*Node, len(ids)),
		addresses:  make(map[string]raft.ServerAddress, len(ids)),
		transports: make(map[string]*raft.InmemTransport, len(ids)),
		stopped:    make(map[string]bool, len(ids)),
	}
	for _, id := range cluster.ids {
		address, transport := raft.NewInmemTransportWithTimeout(
			raft.ServerAddress(id),
			250*time.Millisecond,
		)
		cluster.addresses[id] = address
		cluster.transports[id] = transport
	}
	for _, sourceID := range cluster.ids {
		for _, targetID := range cluster.ids {
			if sourceID != targetID {
				cluster.transports[sourceID].Connect(
					cluster.addresses[targetID],
					cluster.transports[targetID],
				)
			}
		}
	}

	for index, id := range cluster.ids {
		raftConfig := raft.DefaultConfig()
		raftConfig.HeartbeatTimeout = 100 * time.Millisecond
		raftConfig.ElectionTimeout = 100 * time.Millisecond
		raftConfig.LeaderLeaseTimeout = 50 * time.Millisecond
		raftConfig.CommitTimeout = 10 * time.Millisecond
		node, err := NewInMemoryWithTransport(
			Config{
				NodeID:       id,
				Bootstrap:    index == 0,
				ApplyTimeout: 2 * time.Second,
				RaftConfig:   raftConfig,
			},
			clusterstate.NewFSM(clusterstate.NewMachine(nil)),
			cluster.transports[id],
		)
		if err != nil {
			cluster.close(t)
			t.Fatalf("start %s: %v", id, err)
		}
		cluster.nodes[id] = node
	}

	t.Cleanup(func() { cluster.close(t) })

	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]
	for _, id := range cluster.ids {
		if id == leaderID {
			continue
		}
		if err := leader.EnsureVoter(id, string(cluster.addresses[id]), 3*time.Second); err != nil {
			t.Fatalf("add voter %s: %v", id, err)
		}
	}
	return cluster
}

func (c *inMemoryCluster) disconnect(left, right string) {
	c.transports[left].Disconnect(c.addresses[right])
	c.transports[right].Disconnect(c.addresses[left])
}

func (c *inMemoryCluster) close(t *testing.T) {
	t.Helper()
	for _, id := range c.ids {
		if c.stopped[id] {
			continue
		}
		if c.nodes[id] == nil {
			if c.transports[id] != nil {
				_ = c.transports[id].Close()
			}
			c.stopped[id] = true
			continue
		}
		if err := c.nodes[id].Close(); err != nil {
			t.Errorf("close %s: %v", id, err)
		}
		c.stopped[id] = true
	}
}

func (c *inMemoryCluster) stop(t *testing.T, id string) {
	t.Helper()
	if c.stopped[id] {
		return
	}
	if err := c.nodes[id].Close(); err != nil {
		t.Fatalf("stop %s: %v", id, err)
	}
	c.stopped[id] = true
	for _, peerID := range c.ids {
		if peerID != id && !c.stopped[peerID] {
			c.transports[peerID].Disconnect(c.addresses[id])
		}
	}
}

func (c *inMemoryCluster) waitForLeader(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(clusterTestTimeout)
	for time.Now().Before(deadline) {
		leaderID := ""
		leaders := 0
		for _, id := range c.ids {
			if !c.stopped[id] && c.nodes[id].IsLeader() {
				leaderID = id
				leaders++
			}
		}
		if leaders == 1 {
			return leaderID
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for exactly one cluster leader")
	return ""
}

func (c *inMemoryCluster) requireThreeVoters(t *testing.T, leader *Node) {
	t.Helper()
	future := leader.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		t.Fatalf("read raft configuration: %v", err)
	}
	servers := future.Configuration().Servers
	if len(servers) != 3 {
		t.Fatalf("raft server count = %d, want 3", len(servers))
	}
	for _, server := range servers {
		if server.Suffrage != raft.Voter {
			t.Fatalf("server %s suffrage = %s, want voter", server.ID, server.Suffrage)
		}
	}
}

func (c *inMemoryCluster) drainLeadershipChanges() {
	for _, id := range c.ids {
	drain:
		for {
			select {
			case <-c.nodes[id].LeadershipChanges():
			default:
				break drain
			}
		}
	}
}

func (c *inMemoryCluster) applyAddNode(
	t *testing.T,
	leaderID string,
	operationID string,
	nodeID domain.NodeID,
	name string,
) {
	t.Helper()
	command, err := clusterstate.NewCommand(
		operationID,
		clusterstate.CommandAddNode,
		time.Now(),
		domain.Node{ID: nodeID, Name: name},
	)
	if err != nil {
		t.Fatalf("build %s: %v", operationID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := c.nodes[leaderID].Apply(ctx, command)
	if err != nil {
		t.Fatalf("apply %s through %s: %v", operationID, leaderID, err)
	}
	if err := result.Err(); err != nil {
		t.Fatalf("state machine result for %s: %v", operationID, err)
	}
}

func (c *inMemoryCluster) waitForNodeOnActiveMembers(
	t *testing.T,
	nodeID domain.NodeID,
	wantName string,
) {
	t.Helper()
	deadline := time.Now().Add(clusterTestTimeout)
	for time.Now().Before(deadline) {
		allReplicated := true
		for _, id := range c.ids {
			if c.stopped[id] {
				continue
			}
			got, ok := c.nodes[id].State().State().Nodes[nodeID]
			if !ok || got.Name != wantName {
				allReplicated = false
				break
			}
		}
		if allReplicated {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("node %s was not replicated to every active Raft member", nodeID)
}

func TestThreeNodeClusterReplicatesAndFailsOver(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	firstLeaderID := cluster.waitForLeader(t)

	cluster.applyAddNode(t, firstLeaderID, "add-managed-alpha", "managed-alpha", "Alpha")
	cluster.waitForNodeOnActiveMembers(t, "managed-alpha", "Alpha")

	cluster.drainLeadershipChanges()
	cluster.stop(t, firstLeaderID)
	secondLeaderID := cluster.waitForLeader(t)
	if secondLeaderID == firstLeaderID {
		t.Fatalf("leader did not change after stopping %s", firstLeaderID)
	}
	select {
	case becameLeader := <-cluster.nodes[secondLeaderID].LeadershipChanges():
		if !becameLeader {
			t.Fatalf("%s reported loss instead of acquisition of leadership", secondLeaderID)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s did not publish its leadership transition", secondLeaderID)
	}

	cluster.waitForNodeOnActiveMembers(t, "managed-alpha", "Alpha")
	cluster.applyAddNode(t, secondLeaderID, "add-managed-beta", "managed-beta", "Beta")
	cluster.waitForNodeOnActiveMembers(t, "managed-beta", "Beta")

	for _, id := range cluster.ids {
		if cluster.stopped[id] {
			continue
		}
		state := cluster.nodes[id].State().State()
		if len(state.Nodes) != 2 {
			t.Fatalf("%s has %d managed nodes after failover, want 2: %s", id, len(state.Nodes), fmt.Sprint(state.Nodes))
		}
	}
}

func TestTransferLeadershipToResolvesVotingMemberAddress(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	targetID := ""
	for _, id := range cluster.ids {
		if id != leaderID {
			targetID = id
			break
		}
	}
	if err := cluster.nodes[leaderID].TransferLeadershipTo(targetID); err != nil {
		t.Fatal(err)
	}
	if got := cluster.waitForLeader(t); got != targetID {
		t.Fatalf("leader=%s want=%s", got, targetID)
	}
	if err := cluster.nodes[targetID].TransferLeadershipTo("missing"); err == nil {
		t.Fatal("unknown voter accepted")
	}
}
