package durableflow_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"bria/internal/durableflow"
	"bria/internal/messagejournal"
)

func TestRecoveryInputMetadataDoesNotExposePayload(t *testing.T) {
	ctx := context.Background()
	journal := openJournal(t, t.TempDir()+"/journal.json")
	input, _, err := journal.EnqueueInput(ctx, "s", "m", []byte("private payload"))
	if err != nil {
		t.Fatal(err)
	}
	flow, err := durableflow.New(journal, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := flow.RecoveryInputs(ctx, "s")
	if err != nil || len(metadata) != 1 {
		t.Fatalf("RecoveryInputs() = %#v, %v", metadata, err)
	}
	if _, exposed := reflect.TypeOf(metadata[0]).FieldByName("Payload"); exposed {
		t.Fatal("recovery metadata exposes private payload")
	}
	if metadata[0].MessageID != input.MessageID || metadata[0].Sequence != input.Sequence || metadata[0].Phase != messagejournal.InputPending {
		t.Fatalf("recovery metadata = %#v", metadata[0])
	}
}
