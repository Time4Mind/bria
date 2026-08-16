package consensus

import (
	"errors"
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestConfigurationReturnsSnapshotAndEnsureVoterIsIdempotent(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]

	configuration, err := leader.Configuration()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	if len(configuration.Servers) != 3 {
		t.Fatalf("configuration = %v", configuration)
	}
	target := configuration.Servers[0]
	if err := leader.EnsureVoter(string(target.ID), string(target.Address), time.Second); err != nil {
		t.Fatalf("ensure exact voter: %v", err)
	}

	configuration.Servers[0].Address = "mutated"
	fresh, err := leader.Configuration()
	if err != nil {
		t.Fatalf("fresh configuration: %v", err)
	}
	if fresh.Servers[0].Address == "mutated" {
		t.Fatal("caller mutation changed Raft configuration snapshot")
	}
}

func TestEnsureVoterRejectsAddressCollisionAndFollowerCalls(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]
	configuration, err := leader.Configuration()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	if err := leader.EnsureVoter(
		"different-id",
		string(configuration.Servers[0].Address),
		time.Second,
	); err == nil {
		t.Fatal("address collision accepted")
	}

	for _, id := range cluster.ids {
		if id == leaderID {
			continue
		}
		if err := cluster.nodes[id].EnsureVoter("new", "new", time.Second); !errors.Is(err, raft.ErrNotLeader) {
			t.Fatalf("follower EnsureVoter error = %v, want ErrNotLeader", err)
		}
		break
	}
}

func TestEnsureVoterUpdatesSameIDAddress(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]
	targetID := ""
	for _, id := range cluster.ids {
		if id != leaderID {
			targetID = id
			break
		}
	}
	newAddress := raft.ServerAddress(targetID + "-new")
	for _, sourceID := range cluster.ids {
		if sourceID != targetID {
			cluster.transports[sourceID].Connect(newAddress, cluster.transports[targetID])
		}
	}
	if err := leader.EnsureVoter(targetID, string(newAddress), 3*time.Second); err != nil {
		t.Fatalf("update voter address: %v", err)
	}
	configuration, err := leader.Configuration()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	for _, server := range configuration.Servers {
		if string(server.ID) == targetID {
			if server.Address != newAddress || server.Suffrage != raft.Voter {
				t.Fatalf("updated server = %+v", server)
			}
			return
		}
	}
	t.Fatalf("updated server %q missing from configuration", targetID)
}

func TestDemoteVoterKeepsReplicationMember(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]
	targetID := ""
	for _, id := range cluster.ids {
		if id != leaderID {
			targetID = id
			break
		}
	}
	if err := leader.DemoteVoter(targetID, 3*time.Second); err != nil {
		t.Fatalf("demote voter: %v", err)
	}
	configuration, err := leader.Configuration()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	for _, server := range configuration.Servers {
		if string(server.ID) != targetID {
			continue
		}
		if server.Suffrage != raft.Nonvoter {
			t.Fatalf("demoted server = %+v", server)
		}
		if err := leader.EnsureNonvoter(targetID, string(server.Address), time.Second); err != nil {
			t.Fatalf("ensure exact nonvoter: %v", err)
		}
		return
	}
	t.Fatalf("demoted server %q missing", targetID)
}

func TestEnsureNonvoterRefusesImplicitDemotion(t *testing.T) {
	cluster := newThreeNodeCluster(t)
	leaderID := cluster.waitForLeader(t)
	leader := cluster.nodes[leaderID]
	configuration, err := leader.Configuration()
	if err != nil {
		t.Fatalf("configuration: %v", err)
	}
	target := configuration.Servers[0]
	if err := leader.EnsureNonvoter(
		string(target.ID), string(target.Address), time.Second,
	); err == nil {
		t.Fatal("voter was implicitly demoted")
	}
}
