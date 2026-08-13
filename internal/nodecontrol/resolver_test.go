package nodecontrol

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

type resolverState struct{ state *domain.State }

func (r resolverState) State() *domain.State { return r.state }

func TestStateResolverDoesNotFallbackForDisabledOrDeletedNode(t *testing.T) {
	state := domain.NewState()
	state.Nodes["disabled"] = domain.Node{
		ID: "disabled", Name: "Disabled", Lifecycle: domain.NodeDisabled,
		Network: domain.NodeNetwork{ControlAddress: "current:7947"},
	}
	state.NodeTombstones["deleted"] = domain.NodeTombstone{NodeID: "deleted"}
	fallback := NewStaticResolver(map[string]string{
		"disabled": "stale:7947", "deleted": "stale:7947", "bootstrap": "known:7947",
	})
	resolver := NewStateResolver(resolverState{state: state}, fallback)
	for _, nodeID := range []string{"disabled", "deleted"} {
		if address, ok := resolver.ControlAddress(nodeID); ok {
			t.Fatalf("%s resolved through stale fallback as %q", nodeID, address)
		}
	}
	if address, ok := resolver.ControlAddress("bootstrap"); !ok || address != "known:7947" {
		t.Fatalf("bootstrap fallback=%q/%v", address, ok)
	}
}

func TestStateResolverFallsBackForEnabledLegacyNodeWithoutControlAddress(t *testing.T) {
	state := domain.NewState()
	state.Nodes["legacy"] = domain.Node{
		ID: "legacy", Name: "Legacy", Lifecycle: domain.NodeActive,
	}
	resolver := NewStateResolver(resolverState{state: state}, NewStaticResolver(
		map[string]string{"legacy": "configured:7947"},
	))
	if address, ok := resolver.ControlAddress("legacy"); !ok || address != "configured:7947" {
		t.Fatalf("legacy fallback=%q/%v", address, ok)
	}
}

func TestDisabledNodeIsNotAnAuthenticatedClusterMember(t *testing.T) {
	state := domain.NewState()
	state.Nodes["active"] = domain.Node{ID: "active", Name: "Active", Lifecycle: domain.NodeActive}
	state.Nodes["disabled"] = domain.Node{ID: "disabled", Name: "Disabled", Lifecycle: domain.NodeDisabled}
	guard, err := NewStateGuard(resolverState{state: state})
	if err != nil {
		t.Fatal(err)
	}
	if !guard.IsMember("active") || guard.IsMember("disabled") {
		t.Fatal("node lifecycle was not enforced by node-control membership")
	}
}

func TestStateResolverExposesOnlyEnabledNodeFingerprint(t *testing.T) {
	state := domain.NewState()
	state.Nodes["active"] = domain.Node{
		ID: "active", Lifecycle: domain.NodeActive, Fingerprint: "fingerprint",
	}
	state.Nodes["disabled"] = domain.Node{
		ID: "disabled", Lifecycle: domain.NodeDisabled, Fingerprint: "revoked",
	}
	resolver := NewStateResolver(resolverState{state: state}, nil)
	if fingerprint, ok := resolver.NodeFingerprint("active"); !ok || fingerprint != "fingerprint" {
		t.Fatalf("active fingerprint=%q/%v", fingerprint, ok)
	}
	if _, ok := resolver.NodeFingerprint("disabled"); ok {
		t.Fatal("disabled fingerprint exposed")
	}
}

func TestLocalResolverUsesListenerForSelfAndReplicatedAddressForPeers(t *testing.T) {
	state := domain.NewState()
	state.Nodes["self"] = domain.Node{
		ID: "self", Lifecycle: domain.NodeActive, Fingerprint: "self-fingerprint",
		Network: domain.NodeNetwork{ControlAddress: "reverse.example:17947"},
	}
	state.Nodes["peer"] = domain.Node{
		ID: "peer", Lifecycle: domain.NodeActive,
		Network: domain.NodeNetwork{ControlAddress: "peer.internal:7947"},
	}
	delegate := NewStateResolver(resolverState{state: state}, nil)
	resolver := NewLocalResolver("self", "127.0.0.1:7947", delegate)
	if address, ok := resolver.ControlAddress("self"); !ok || address != "127.0.0.1:7947" {
		t.Fatalf("self address=%q/%v", address, ok)
	}
	if address, ok := resolver.ControlAddress("peer"); !ok || address != "peer.internal:7947" {
		t.Fatalf("peer address=%q/%v", address, ok)
	}
	if fingerprint, ok := resolver.NodeFingerprint("self"); !ok || fingerprint != "self-fingerprint" {
		t.Fatalf("self fingerprint=%q/%v", fingerprint, ok)
	}
}
