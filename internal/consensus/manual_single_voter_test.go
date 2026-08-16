package consensus

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestSoleVoterLeaderCommitsAfterAllReplicasDisconnect(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]

	for _, id := range cluster.ids {
		if id == leaderID {
			continue
		}
		if err := leader.DemoteVoter(id, 3*time.Second); err != nil {
			t.Fatalf("demote %s: %v", id, err)
		}
	}
	configuration, err := leader.Configuration()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	voters := 0
	for _, server := range configuration.Servers {
		if server.Suffrage == raft.Voter {
			voters++
			if string(server.ID) != leaderID {
				t.Fatalf("unexpected voter after convergence: %+v", server)
			}
		}
	}
	if voters != 1 {
		t.Fatalf("voter count=%d, want 1", voters)
	}

	for _, id := range cluster.ids {
		if id != leaderID {
			cluster.disconnect(leaderID, id)
		}
	}
	time.Sleep(250 * time.Millisecond)
	if !leader.IsLeader() {
		t.Fatal("sole voter lost leadership after replicas disconnected")
	}
	cluster.applyAddNode(t, leaderID, "single-voter-write", "managed", "Managed")
}
