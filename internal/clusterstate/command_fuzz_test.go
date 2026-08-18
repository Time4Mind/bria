package clusterstate

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func FuzzMachineRejectsMalformedCommandsWithoutMutatingState(f *testing.F) {
	f.Add([]byte(`{"id":"n","name":"Node"}`), "unknown", "operation")
	f.Add([]byte("not-json"), string(CommandAddNode), "operation")
	f.Fuzz(func(t *testing.T, payload []byte, kind, operationID string) {
		if len(payload) > 1<<20 || len(kind) > 256 || len(operationID) > 512 {
			t.Skip()
		}
		machine := NewMachine(nil)
		before := machine.State()
		result := machine.Apply(Command{
			Version: CommandVersion, OperationID: operationID,
			Kind: CommandKind(kind), IssuedAt: time.Unix(1, 0).UTC(),
			Payload: json.RawMessage(payload),
		})
		if result.Err() != nil && !reflect.DeepEqual(machine.State(), before) {
			t.Fatal("failed command mutated state")
		}
	})
}
