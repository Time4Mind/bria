package executor_test

import (
	"context"
	"testing"

	"bria/internal/computer"
	"bria/internal/executor"
	"bria/internal/nodelink"
)

func TestNodeEnvelopeMappingPreservesRequestAndProcessorDeduplicates(t *testing.T) {
	request := executor.Request{OperationID: "operation-1", Generation: 3, SessionID: "session-1", Action: executor.ActionSubmit, Payload: []byte("hello")}
	envelope, err := executor.EncodeRequestEnvelope("coordinator-1", "executor-1", request)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := computer.RestoreFence(computer.FenceSnapshot{CoordinatorID: "coordinator-1", Generation: 3})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := nodelink.NewProcessor("executor-1", fence, nodelink.NewMemoryOperationLedger())
	if err != nil {
		t.Fatal(err)
	}
	identity := nodelink.ChannelIdentity{LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true}
	applied := 0
	apply := func(_ context.Context, got nodelink.Envelope) error {
		decoded, err := executor.DecodeRequestEnvelope(got)
		if err != nil {
			return err
		}
		if decoded.OperationID != request.OperationID || decoded.Generation != request.Generation || decoded.SessionID != request.SessionID || decoded.Action != request.Action || string(decoded.Payload) != "hello" {
			t.Fatalf("decoded request = %#v", decoded)
		}
		applied++
		return nil
	}
	first, err := processor.Process(context.Background(), identity, envelope, apply)
	if err != nil || first.Duplicate {
		t.Fatalf("first process = %#v, %v", first, err)
	}
	second, err := processor.Process(context.Background(), identity, envelope, apply)
	if err != nil || !second.Duplicate {
		t.Fatalf("second process = %#v, %v", second, err)
	}
	if applied != 1 {
		t.Fatalf("apply count = %d, want 1", applied)
	}
}

func TestNodeResponseEnvelopeRoundTripsToOriginalOperation(t *testing.T) {
	request := executor.Request{OperationID: "history-1", Generation: 2, SessionID: "session-1", Action: executor.ActionHistory}
	command, err := executor.EncodeRequestEnvelope("coordinator-1", "executor-1", request)
	if err != nil {
		t.Fatal(err)
	}
	response := executor.Response{OperationID: request.OperationID, Accepted: true, Payload: []byte("history")}
	acknowledgement, err := executor.EncodeResponseEnvelope(command, response)
	if err != nil {
		t.Fatal(err)
	}
	got, err := executor.DecodeResponseEnvelope(request, acknowledgement)
	if err != nil {
		t.Fatal(err)
	}
	if got.OperationID != response.OperationID || !got.Accepted || string(got.Payload) != "history" {
		t.Fatalf("decoded response = %#v", got)
	}
}
