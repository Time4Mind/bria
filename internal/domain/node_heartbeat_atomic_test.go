package domain_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestInvalidInteractiveHeartbeatDoesNotPartiallyMutateNode(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	before := state.Nodes["node"]
	_, err := state.PublishNodeHeartbeat(
		"node", "boot", "changed", "", "", "", "", nil, nil, []domain.InteractivePromptReport{{
			SessionID: "session", Generation: 1, Present: true, Kind: "permission", Hash: "bad",
		}}, nil, time.Unix(100, 0).UTC(),
	)
	if err == nil || !reflect.DeepEqual(state.Nodes["node"], before) {
		t.Fatalf("err=%v node=%#v", err, state.Nodes["node"])
	}
}

func TestLateHeartbeatValidationDoesNotPartiallyMutateState(t *testing.T) {
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOffline}); err != nil {
		t.Fatal(err)
	}
	before := state.Clone()
	_, err := state.PublishNodeHeartbeat(
		"node", "boot", "changed", "linux", "", strings.Repeat("a", 64), "",
		[]domain.BackendDescriptor{{Name: "codex"}}, nil, nil, nil,
		time.Unix(100, 0).UTC(),
	)
	if err == nil || !reflect.DeepEqual(state, before) {
		t.Fatalf("err=%v state changed after rejected heartbeat", err)
	}
}
