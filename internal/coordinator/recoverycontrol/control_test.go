package recoverycontrol_test

import (
	"testing"

	"bria/internal/coordinator/recoverycontrol"
)

func TestValidateRequiresExactUpdateAndDistinctDurableOperations(t *testing.T) {
	valid := recoverycontrol.Control{OriginalOperationID: "status:7", PromptOperationID: "recovery:callback:7", UpdateID: 7}
	if err := recoverycontrol.Validate(valid, 7); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []recoverycontrol.Control{
		{},
		{OriginalOperationID: "status:7", PromptOperationID: "status:7", UpdateID: 7},
		{OriginalOperationID: "status:7", PromptOperationID: "recovery:callback:7", UpdateID: 8},
	} {
		if err := recoverycontrol.Validate(invalid, 7); err == nil {
			t.Fatalf("invalid control accepted: %#v", invalid)
		}
	}
}
