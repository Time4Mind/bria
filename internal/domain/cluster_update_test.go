package domain_test

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestClusterUpdateTransitionsAreStrictAndCloneIsIndependent(t *testing.T) {
	state := domain.NewState()
	for _, id := range []domain.NodeID{"one", "two"} {
		if err := state.AddNode(domain.Node{ID: id, Name: string(id), Lifecycle: domain.NodeActive}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Unix(10, 0)
	err := state.BeginClusterUpdate(domain.ClusterUpdate{
		ID: "job", Version: "v2",
		ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Order:          []domain.NodeID{"one", "two"},
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.SetClusterUpdateNode("job", "one", domain.NodeUpdateHealthy, "", at); err == nil {
		t.Fatal("pending node skipped the installing phase")
	}
	if err := state.SetClusterUpdateNode("job", "one", domain.NodeUpdateInstalling, "", at); err != nil {
		t.Fatal(err)
	}
	if err := state.SetClusterUpdateNode("job", "one", domain.NodeUpdateHealthy, "", at); err != nil {
		t.Fatal(err)
	}
	clone := state.Clone()
	clone.ClusterUpdate.Nodes["one"] = domain.NodeUpdate{Phase: domain.NodeUpdateFailed}
	if state.ClusterUpdate.Nodes["one"].Phase != domain.NodeUpdateHealthy {
		t.Fatal("state clone shares cluster update node map")
	}
}
