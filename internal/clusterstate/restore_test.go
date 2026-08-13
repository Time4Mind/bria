package clusterstate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

func TestRestoreClusterInstallsSnapshotOnlyIntoEmptyMachineAndDeduplicates(t *testing.T) {
	restored := domain.NewState()
	source := clusterstate.NewMachine(restored)
	addNode, err := clusterstate.NewCommand(
		"existing-operation", clusterstate.CommandAddNode, time.Now(),
		domain.Node{ID: "node", Name: "Restored"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := source.Apply(addNode); result.Err() != nil {
		t.Fatal(result.Err())
	}
	snapshot, err := source.MarshalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(snapshot)
	machine := clusterstate.NewMachine(nil)
	command, err := clusterstate.NewCommand(
		"restore-backup", clusterstate.CommandRestoreCluster, time.Now(),
		clusterstate.RestoreCluster{
			BackupSHA256: hex.EncodeToString(digest[:]), Snapshot: snapshot,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	if machine.State().Nodes["node"].Name != "Restored" {
		t.Fatal("restored node missing")
	}
	if result := machine.Apply(addNode); result.Err() != nil {
		t.Fatalf("restored operation ledger was not deduplicated: %v", result.Err())
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatalf("idempotent replay failed: %v", result.Err())
	}
	command.OperationID = "different-restore"
	if result := machine.Apply(command); result.Err() == nil {
		t.Fatal("second distinct restore replaced non-empty state")
	}
}
