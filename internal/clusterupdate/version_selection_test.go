package clusterupdate

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestReleaseNeedsUpdateRejectsDowngrades(t *testing.T) {
	tests := []struct {
		name    string
		current string
		target  string
		want    bool
	}{
		{name: "same release", current: "v0.1.29", target: "v0.1.29", want: false},
		{name: "git describe ahead of same tag", current: "v0.1.29-48-g79bf70a", target: "v0.1.29", want: false},
		{name: "dirty git describe ahead of same tag", current: "v0.1.29-48-g79bf70a-dirty", target: "v0.1.29", want: false},
		{name: "newer release", current: "v0.1.30", target: "v0.1.29", want: false},
		{name: "next release", current: "v0.1.29-48-g79bf70a", target: "v0.1.30", want: true},
		{name: "older release", current: "v0.1.28", target: "v0.1.29", want: true},
		{name: "opaque legacy version", current: "v1", target: "v2", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := releaseNeedsUpdate(test.current, test.target); got != test.want {
				t.Fatalf("releaseNeedsUpdate(%q, %q) = %t, want %t", test.current, test.target, got, test.want)
			}
		})
	}
}

func TestCoordinatorExcludesNewerDevelopmentBuildFromRollout(t *testing.T) {
	state := domain.NewState()
	for index, node := range []domain.Node{
		{ID: "leader", Name: "leader", Version: "v0.1.28"},
		{ID: "ahead", Name: "ahead", Version: "v0.1.29-48-g79bf70a"},
	} {
		node.Status, node.Lifecycle = domain.NodeOnline, domain.NodeActive
		node.OS, node.Arch = "linux", "amd64"
		node.CreatedAt = time.Unix(int64(index+1), 0)
		if err := state.AddNode(node); err != nil {
			t.Fatal(err)
		}
	}
	machine := clusterstate.NewMachine(state)
	consensus := &coordinatorConsensus{machine: machine, leader: "leader", local: "leader"}
	nodes := &coordinatorNodes{manifest: VerifiedManifest{
		Manifest: Manifest{Version: "v0.1.29", Artifacts: []Artifact{{OS: "linux", Arch: "amd64"}}},
		SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	coordinator, err := NewCoordinator("leader", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	update, err := coordinator.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Order) != 1 || update.Order[0] != "leader" {
		t.Fatalf("update order = %v, want only older leader", update.Order)
	}
}

func TestCoordinatorRejectsReleaseBehindDevelopmentBuild(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{
		ID: "leader", Name: "leader", Status: domain.NodeOnline, Lifecycle: domain.NodeActive,
		Version: "v0.1.29-48-g79bf70a", OS: "linux", Arch: "amd64", CreatedAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	consensus := &coordinatorConsensus{machine: machine, leader: "leader", local: "leader"}
	nodes := &coordinatorNodes{manifest: VerifiedManifest{
		Manifest: Manifest{Version: "v0.1.29", Artifacts: []Artifact{{OS: "linux", Arch: "amd64"}}},
		SHA256:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	coordinator, err := NewCoordinator("leader", machine, consensus, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Start(context.Background()); err == nil {
		t.Fatal("development build was downgraded to its base release")
	}
	if machine.State().ClusterUpdate != nil {
		t.Fatal("rejected downgrade changed cluster state")
	}
}
