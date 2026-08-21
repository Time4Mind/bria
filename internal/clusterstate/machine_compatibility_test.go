package clusterstate_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/clusterupdate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestCommandVersionNeverExceedsPreRaftNodeProtocol(t *testing.T) {
	if clusterstate.CommandVersion > clusterupdate.NodeProtocolVersion {
		t.Fatalf("command version %d exceeds node protocol %d",
			clusterstate.CommandVersion, clusterupdate.NodeProtocolVersion)
	}
}

func TestRollingVersionBoundaryRejectsUnknownCommandWithoutMutatingState(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	baseline := command(t, "baseline", clusterstate.CommandAddNode,
		domain.Node{ID: "alpha", Name: "Alpha"})
	if result := machine.Apply(baseline); result.Err() != nil {
		t.Fatal(result.Err())
	}
	future := command(t, "future", clusterstate.CommandAddNode,
		domain.Node{ID: "beta", Name: "Beta"})
	future.Version = clusterstate.CommandVersion + 1
	first := machine.Apply(future)
	if first.Err() == nil || !strings.Contains(first.Error, "unsupported command version") {
		t.Fatalf("future result=%#v", first)
	}
	if _, exists := machine.State().Nodes["beta"]; exists {
		t.Fatal("future command partially mutated old state machine")
	}
	if duplicate := machine.Apply(future); duplicate.Error != first.Error {
		t.Fatalf("future command outcome was not deterministic: %#v / %#v", first, duplicate)
	}
	current := command(t, "current", clusterstate.CommandAddNode,
		domain.Node{ID: "gamma", Name: "Gamma"})
	if result := machine.Apply(current); result.Err() != nil {
		t.Fatalf("current command after future rejection: %v", result.Err())
	}
}

func TestCurrentMachineReplaysPreviousCommandVersion(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	legacy := command(t, "legacy-version", clusterstate.CommandAddNode,
		domain.Node{ID: "legacy", Name: "Legacy"})
	legacy.Version = clusterstate.CommandVersion - 1
	if result := machine.Apply(legacy); result.Err() != nil {
		t.Fatalf("previous command version: %v", result.Err())
	}
	if _, ok := machine.State().Nodes["legacy"]; !ok {
		t.Fatal("previous command version did not mutate state")
	}
}

func TestStrictPayloadRejectsUnknownFieldsButLegacyReplayRemainsReadable(t *testing.T) {
	machine := clusterstate.NewMachine(nil)
	strict := command(t, "strict", clusterstate.CommandAddNode,
		domain.Node{ID: "strict", Name: "Strict"})
	var payload map[string]any
	if err := json.Unmarshal(strict.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["future_semantics"] = true
	strict.Payload, _ = json.Marshal(payload)
	if result := machine.Apply(strict); result.Err() == nil ||
		!strings.Contains(result.Error, "unknown field") {
		t.Fatalf("strict payload result=%#v", result)
	}
	if _, exists := machine.State().Nodes["strict"]; exists {
		t.Fatal("strict payload partially mutated state")
	}

	legacy := command(t, "legacy", clusterstate.CommandAddNode,
		domain.Node{ID: "legacy", Name: "Legacy"})
	legacy.StrictPayload = false
	if err := json.Unmarshal(legacy.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["removed_legacy_field"] = "retained log data"
	legacy.Payload, _ = json.Marshal(payload)
	if result := machine.Apply(legacy); result.Err() != nil {
		t.Fatalf("legacy replay: %v", result.Err())
	}
	if got := machine.State().Nodes["legacy"].Name; got != "Legacy" {
		t.Fatalf("legacy node name=%q", got)
	}
}

func TestStrictPreferenceReplayAcceptsDeprecatedExpiryFieldAndCanonicalizesIt(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "alpha", Name: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "alpha"); err != nil {
		t.Fatal(err)
	}
	preferences := domain.DefaultUserPreferences()
	preferences.ArchiveExpiryAction = domain.ArchiveRemoveRecord
	legacy := command(t, "legacy-preferences", clusterstate.CommandSetPreferences,
		clusterstate.SetPreferences{UserID: 7, Preferences: preferences})
	machine := clusterstate.NewMachine(state)
	result := machine.Apply(legacy)
	if result.Err() != nil {
		t.Fatal(result.Err())
	}
	if got := machine.State().Preferences[7].ArchiveExpiryAction; got != domain.ArchiveRemoveAll {
		t.Fatalf("legacy expiry action=%q, want canonical %q", got, domain.ArchiveRemoveAll)
	}
}
