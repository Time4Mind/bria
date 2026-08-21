package domain_test

import (
	"testing"
	"time"
)

func TestDarwinBootIdentityIgnoresLegacyMicrosecondDrift(t *testing.T) {
	for _, current := range []string{
		"darwin:1787239247:341351",
		"darwin:1787239247",
	} {
		t.Run(current, func(t *testing.T) {
			state := fixtureState(t)
			node := state.Nodes["alpha"]
			node.BootID = "darwin:1787239247:256083"
			state.Nodes["alpha"] = node
			ref := addSession(t, state, "unbound", "alpha", 1, time.Unix(100, 0).UTC())
			session := state.Sessions[ref.Key()]
			session.ProviderSessionID = ""
			state.Sessions[ref.Key()] = session

			plan, err := state.ObserveNodeBoot("alpha", current, time.Unix(200, 0).UTC())
			if err != nil || len(plan.Recover)+len(plan.Archived)+len(plan.Discarded) != 0 {
				t.Fatalf("same Darwin boot plan=(%#v, %v)", plan, err)
			}
			if got := state.Sessions[ref.Key()]; !got.IsLive() || got.ResumePending {
				t.Fatalf("same-boot update changed session=%#v", got)
			}
			if got := state.Nodes["alpha"].BootID; got != current {
				t.Fatalf("stored boot id=%q, want %q", got, current)
			}
		})
	}
}
