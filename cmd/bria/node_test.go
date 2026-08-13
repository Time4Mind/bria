package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/consensus"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func TestRegisterLocalNodeBootstrapsConfiguredOwner(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	node, err := consensus.NewInMemory(
		consensus.Config{NodeID: "local", ApplyTimeout: time.Second},
		clusterstate.NewFSM(machine),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := node.WaitForLeader(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := registerLocalNode(ctx, node, config.Config{
		NodeID: "local", NodeName: "Local", BootstrapOwnerID: 77,
	}, "fingerprint", runtimehost.ExecCommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Recover) != 0 || len(plan.Archived) != 0 {
		t.Fatalf("initial boot plan=%#v", plan)
	}
	state := node.State().State()
	if registered := state.Nodes["local"]; registered.Status != domain.NodeOnline ||
		registered.LastSeenAt.IsZero() || registered.Fingerprint != "fingerprint" {
		t.Fatalf("registered runtime=%#v", registered)
	}
	access, ok := state.Users[77]
	if !ok || access.Role != domain.RoleOwner || !access.AllowedNodes["local"] {
		t.Fatalf("bootstrap owner access=%#v", access)
	}
	if !reflect.DeepEqual(state.Preferences[77], domain.DefaultUserPreferences()) {
		t.Fatalf("bootstrap owner preferences=%#v", state.Preferences[77])
	}
}
