package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/hashicorp/raft"
)

func TestConfiguredPeersAreSeedsAfterDynamicMembership(t *testing.T) {
	configured := config.Config{RaftPeers: []config.RaftPeer{
		{NodeID: "alpha", Address: "old-alpha.bria.internal:7946"},
		{NodeID: "beta", Address: "beta.bria.internal:7946"},
	}}
	current := raft.Configuration{Servers: []raft.Server{
		{ID: "alpha", Address: "relocated-alpha.bria.internal:17946", Suffrage: raft.Voter},
		{ID: "beta", Address: "beta.bria.internal:7946", Suffrage: raft.Voter},
		{ID: "gamma", Address: "gamma.bria.internal:7946", Suffrage: raft.Voter},
	}}
	missing := missingConfiguredVoters(current, configured)
	if len(missing) != 0 {
		t.Fatalf("relocated and dynamically added voters treated as drift: %#v", missing)
	}

	current.Servers = current.Servers[:1]
	missing = missingConfiguredVoters(current, configured)
	if len(missing) != 1 || missing[0].NodeID != "beta" {
		t.Fatalf("missing bootstrap seed=%#v", missing)
	}
}
