package consensus

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestThreeNodePartitionLeavesOnlyMajorityLeader(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	oldLeader := cluster.waitForLeader(t)
	for _, id := range cluster.ids {
		if id != oldLeader {
			cluster.disconnect(oldLeader, id)
		}
	}
	partitionObserved := false
	deadline := time.Now().Add(clusterTestTimeout)
	for time.Now().Before(deadline) {
		majorityLeader := ""
		for _, id := range cluster.ids {
			if id != oldLeader && cluster.nodes[id].IsLeader() {
				majorityLeader = id
			}
		}
		if !cluster.nodes[oldLeader].IsLeader() &&
			cluster.nodes[oldLeader].LeaderID() == "" && majorityLeader != "" {
			partitionObserved = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !partitionObserved {
		t.Fatal("isolated leader did not step down while the majority elected a leader")
	}
	for _, id := range cluster.ids {
		if id != oldLeader {
			cluster.reconnect(oldLeader, id)
		}
	}
	cluster.waitForSharedLeaderIdentity(t)
}

func TestTwoNodePartitionHasNoLeaderAndCannotCommitChoice(t *testing.T) {
	cluster := newTwoNodeCluster(t)
	cluster.waitForLeader(t)
	cluster.disconnect(cluster.ids[0], cluster.ids[1])
	deadline := time.Now().Add(clusterTestTimeout)
	for time.Now().Before(deadline) {
		if !cluster.nodes[cluster.ids[0]].IsLeader() &&
			cluster.nodes[cluster.ids[0]].LeaderID() == "" &&
			!cluster.nodes[cluster.ids[1]].IsLeader() &&
			cluster.nodes[cluster.ids[1]].LeaderID() == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cluster.nodes[cluster.ids[0]].IsLeader() || cluster.nodes[cluster.ids[1]].IsLeader() {
		t.Fatal("two-node partition retained a leader without quorum")
	}
	if cluster.nodes[cluster.ids[0]].LeaderID() != "" ||
		cluster.nodes[cluster.ids[1]].LeaderID() != "" {
		t.Fatal("partitioned nodes retained a stale leader identity")
	}
	for _, id := range cluster.ids {
		command, err := clusterstate.NewCommand(
			"partition-choice-"+id, clusterstate.CommandAddNode, time.Now(),
			domain.Node{ID: domain.NodeID("choice-" + id), Name: "Choice"},
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, applyErr := cluster.nodes[id].Apply(ctx, command)
		cancel()
		if applyErr == nil {
			t.Fatalf("partitioned node %s committed a manual choice without quorum", id)
		}
	}
	cluster.reconnect(cluster.ids[0], cluster.ids[1])
	cluster.waitForSharedLeaderIdentity(t)
}

func (c *inMemoryCluster) reconnect(left, right string) {
	c.transports[left].Connect(c.addresses[right], c.transports[right])
	c.transports[right].Connect(c.addresses[left], c.transports[left])
}

func (c *inMemoryCluster) waitForSharedLeaderIdentity(t *testing.T) {
	t.Helper()
	leaderID := c.waitForLeader(t)
	deadline := time.Now().Add(clusterTestTimeout)
	for time.Now().Before(deadline) {
		shared := true
		for _, id := range c.ids {
			if !c.stopped[id] && c.nodes[id].LeaderID() != leaderID {
				shared = false
				break
			}
		}
		if shared {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("leader identity %s did not propagate after reconnect", leaderID)
}
