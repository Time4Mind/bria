package updateflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"bria/internal/update"
	"bria/internal/updateflow"
)

func TestFileStoreRoundTripsPreparedStateAcrossReopen(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "update-flow")
	store, err := updateflow.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	state := updateflow.State{
		FormatVersion:  updateflow.StateFormatVersion,
		Revision:       1,
		FlowID:         "flow-reopen",
		SignedManifest: []byte(`{"signed":true}`),
		VerifiedKeyID:  "owner",
		Manifest: update.Manifest{FormatVersion: update.ManifestFormatVersion, Version: "2.0.0", Artifacts: []update.Artifact{{
			Name: "bria-linux-amd64", Platform: "linux", Arch: "amd64", Size: 0,
			SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		}}},
		Rollout: update.Rollout{TargetVersion: "2.0.0", RolloutState: update.RolloutRunning, OrderedNodes: []update.Node{{
			ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0",
			Availability: update.AvailabilityOnline, State: update.NodePending,
		}}},
		Targets: []updateflow.Target{{
			Node:     update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline},
			Platform: "linux", Arch: "amd64", PriorState: "state-v1",
		}},
		StageOperations: map[string]updateflow.StageOperation{},
		Phase:           updateflow.PhasePrepared,
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reopened, err := updateflow.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	loaded, found, err := reopened.Load(context.Background(), state.FlowID)
	if err != nil || !found {
		t.Fatalf("Load = found %v, err %v", found, err)
	}
	if !reflect.DeepEqual(loaded, state) {
		t.Fatalf("loaded = %#v, want %#v", loaded, state)
	}
}

func TestFileStoreRejectsStaleConcurrentRevisionAcrossReopenedStores(t *testing.T) {
	t.Parallel()
	directory := filepath.Join(t.TempDir(), "update-flow")
	firstStore, err := updateflow.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	state := preparedCoordinatorState("revision-flow")
	if err := firstStore.Save(context.Background(), state); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	secondStore, err := updateflow.OpenFileStore(directory)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	first, _, err := firstStore.Load(context.Background(), state.FlowID)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := secondStore.Load(context.Background(), state.FlowID)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Rollout.SetAvailability("coordinator", update.AvailabilityOffline); err != nil {
		t.Fatal(err)
	}
	first.Revision++
	if err := firstStore.Save(context.Background(), first); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := second.Rollout.SetAvailability("coordinator", update.AvailabilityUnknown); err != nil {
		t.Fatal(err)
	}
	second.Revision++
	if err := secondStore.Save(context.Background(), second); !errors.Is(err, updateflow.ErrRevisionConflict) {
		t.Fatalf("stale Save error = %v, want ErrRevisionConflict", err)
	}
}

func preparedCoordinatorState(flowID string) updateflow.State {
	artifact := update.Artifact{
		Name: "bria-linux-amd64", Platform: "linux", Arch: "amd64", Size: 0,
		SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	node := update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline, State: update.NodePending}
	return updateflow.State{
		FormatVersion: updateflow.StateFormatVersion, Revision: 1, FlowID: flowID,
		SignedManifest: []byte(`{"signed":true}`), VerifiedKeyID: "owner",
		Manifest:        update.Manifest{FormatVersion: update.ManifestFormatVersion, Version: "2.0.0", Artifacts: []update.Artifact{artifact}},
		Rollout:         update.Rollout{TargetVersion: "2.0.0", RolloutState: update.RolloutRunning, OrderedNodes: []update.Node{node}},
		Targets:         []updateflow.Target{{Node: node, Platform: "linux", Arch: "amd64", PriorState: "state-v1"}},
		StageOperations: map[string]updateflow.StageOperation{}, Phase: updateflow.PhasePrepared,
	}
}

func TestFileStoreRejectsCorruptRolloutThatMovesCoordinatorBeforeExecutor(t *testing.T) {
	t.Parallel()
	store, err := updateflow.OpenFileStore(filepath.Join(t.TempDir(), "update-flow"))
	if err != nil {
		t.Fatalf("OpenFileStore: %v", err)
	}
	artifact := update.Artifact{
		Name: "bria-linux-amd64", Platform: "linux", Arch: "amd64", Size: 0,
		SHA256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	coordinator := update.Node{ID: "coordinator", Role: update.RoleCoordinator, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline, State: update.NodePending}
	executor := update.Node{ID: "executor", Role: update.RoleExecutor, CurrentVersion: "1.0.0", Availability: update.AvailabilityOnline, State: update.NodePending}
	state := updateflow.State{
		FormatVersion: updateflow.StateFormatVersion, Revision: 1, FlowID: "corrupt-order",
		SignedManifest: []byte(`{"signed":true}`), VerifiedKeyID: "owner",
		Manifest: update.Manifest{FormatVersion: update.ManifestFormatVersion, Version: "2.0.0", Artifacts: []update.Artifact{artifact}},
		Rollout: update.Rollout{
			TargetVersion: "2.0.0", RolloutState: update.RolloutRunning,
			OrderedNodes: []update.Node{coordinator, executor},
		},
		Targets: []updateflow.Target{
			{Node: coordinator, Platform: "linux", Arch: "amd64", PriorState: "coordinator-state-v1"},
			{Node: executor, Platform: "linux", Arch: "amd64", PriorState: "executor-state-v1"},
		},
		StageOperations: map[string]updateflow.StageOperation{}, Phase: updateflow.PhasePrepared,
	}

	if err := store.Save(context.Background(), state); !errors.Is(err, updateflow.ErrInvalidRequest) {
		t.Fatalf("Save error = %v, want ErrInvalidRequest", err)
	}
}
