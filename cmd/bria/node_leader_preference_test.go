package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestAdapterLeadershipPolicy(t *testing.T) {
	tests := []struct {
		name   string
		local  domain.NodeID
		leader domain.NodeID
		policy domain.LeaderPolicy
		want   bool
	}{
		{name: "manual assigned leader", local: "android", leader: "android", policy: domain.LeaderPolicy{NodeID: "android"}, want: true},
		{name: "manual non leader waits", local: "linux", leader: "linux", policy: domain.LeaderPolicy{NodeID: "android"}},
		{name: "manual setup follows consensus", local: "android", leader: "android", policy: domain.LeaderPolicy{}, want: true},
		{name: "automatic follows consensus", local: "linux", leader: "linux", policy: domain.LeaderPolicy{Mode: domain.LeaderSelectionAutomatic}, want: true},
		{name: "follower never exposes adapter", local: "linux", leader: "android", policy: domain.LeaderPolicy{Mode: domain.LeaderSelectionAutomatic}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := adapterLeadershipAllowed(test.local, test.leader, test.policy); got != test.want {
				t.Fatalf("allowed=%v want=%v", got, test.want)
			}
		})
	}
}
