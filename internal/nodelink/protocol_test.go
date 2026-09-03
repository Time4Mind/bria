package nodelink_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"bria/internal/computer"
	"bria/internal/domain"
	"bria/internal/nodelink"
)

func TestProcessorRejectsWrongIdentityAndVersionWithoutApplying(t *testing.T) {
	processor := mustProcessor(t, "executor-1")
	applied := 0
	apply := func(context.Context, nodelink.Envelope) error { applied++; return nil }
	envelope := commandEnvelope()

	_, err := processor.Process(context.Background(), nodelink.ChannelIdentity{LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true}, withSource(envelope, "impostor"), apply)
	if !errors.Is(err, nodelink.ErrWrongIdentity) {
		t.Fatalf("wrong identity error = %v", err)
	}
	_, err = processor.Process(context.Background(), nodelink.ChannelIdentity{LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true}, withVersion(envelope, nodelink.ProtocolVersion+1), apply)
	if !errors.Is(err, nodelink.ErrIncompatibleVersion) {
		t.Fatalf("version error = %v", err)
	}
	if applied != 0 {
		t.Fatalf("apply called %d times after rejected envelopes", applied)
	}
}

func TestProcessorRejectsStaleGenerationAndDeduplicatesOperation(t *testing.T) {
	fence, err := computer.RestoreFence(computer.FenceSnapshot{CoordinatorID: "coordinator-1", Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := nodelink.NewProcessor("executor-1", fence, nodelink.NewMemoryOperationLedger())
	if err != nil {
		t.Fatal(err)
	}
	identity := nodelink.ChannelIdentity{LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true}
	applied := 0
	apply := func(context.Context, nodelink.Envelope) error { applied++; return nil }

	first := commandEnvelope()
	first.Generation = 2
	result, err := processor.Process(context.Background(), identity, first, apply)
	if err != nil || result.Duplicate {
		t.Fatalf("first process = %#v, %v", result, err)
	}
	result, err = processor.Process(context.Background(), identity, first, apply)
	if err != nil || !result.Duplicate {
		t.Fatalf("duplicate process = %#v, %v", result, err)
	}
	stale := first
	stale.OperationID = "operation-2"
	stale.Generation = 1
	if _, err := processor.Process(context.Background(), identity, stale, apply); !errors.Is(err, computer.ErrStaleGeneration) {
		t.Fatalf("stale generation error = %v", err)
	}
	if applied != 1 {
		t.Fatalf("apply called %d times, want once", applied)
	}
}

func TestProcessorRejectsOperationIDReusedForDifferentCommand(t *testing.T) {
	processor := mustProcessor(t, "executor-1")
	identity := nodelink.ChannelIdentity{LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true}
	apply := func(context.Context, nodelink.Envelope) error { return nil }
	first := commandEnvelope()
	if _, err := processor.Process(context.Background(), identity, first, apply); err != nil {
		t.Fatal(err)
	}
	conflict := first
	conflict.Payload = []byte(`{"text":"different"}`)
	if _, err := processor.Process(context.Background(), identity, conflict, apply); !errors.Is(err, nodelink.ErrOperationConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestProcessorAcceptsExecutorEventUnderAcceptedCoordinatorTerm(t *testing.T) {
	fence, err := computer.RestoreFence(computer.FenceSnapshot{CoordinatorID: "coordinator-1", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := nodelink.NewProcessor("coordinator-1", fence, nodelink.NewMemoryOperationLedger())
	if err != nil {
		t.Fatal(err)
	}
	event := nodelink.Envelope{
		Version:          nodelink.ProtocolVersion,
		Kind:             nodelink.KindEvent,
		OperationID:      "event-1",
		Generation:       1,
		CoordinatorID:    "coordinator-1",
		SourceComputerID: "executor-1",
		TargetComputerID: "coordinator-1",
		Payload:          []byte(`{"kind":"final"}`),
	}
	applied := 0
	result, err := processor.Process(context.Background(), nodelink.ChannelIdentity{
		LocalComputerID: "coordinator-1", PeerComputerID: "executor-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true,
	}, event, func(context.Context, nodelink.Envelope) error { applied++; return nil })
	if err != nil || result.Duplicate || applied != 1 {
		t.Fatalf("event process = %#v, applied=%d, err=%v", result, applied, err)
	}
}

func TestProcessorRejectsCommandFromAuthenticatedNonCoordinator(t *testing.T) {
	processor := mustProcessor(t, "executor-1")
	command := commandEnvelope()
	command.SourceComputerID = "executor-2"
	applied := 0
	_, err := processor.Process(context.Background(), nodelink.ChannelIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "executor-2", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true,
	}, command, func(context.Context, nodelink.Envelope) error { applied++; return nil })
	if !errors.Is(err, nodelink.ErrWrongIdentity) {
		t.Fatalf("command error = %v, want ErrWrongIdentity", err)
	}
	if applied != 0 {
		t.Fatalf("unauthorized command applied %d times", applied)
	}
}

func TestProcessorRejectsChannelNotInitiatedByExecutor(t *testing.T) {
	processor := mustProcessor(t, "executor-1")
	applied := 0
	_, err := processor.Process(context.Background(), nodelink.ChannelIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "coordinator-1", MutuallyAuthenticated: true,
	}, commandEnvelope(), func(context.Context, nodelink.Envelope) error { applied++; return nil })
	if !errors.Is(err, nodelink.ErrWrongIdentity) {
		t.Fatalf("channel error = %v, want ErrWrongIdentity", err)
	}
	if applied != 0 {
		t.Fatalf("coordinator-initiated command applied %d times", applied)
	}
}

func TestProcessorCannotAdvanceCoordinatorTermFromOrdinaryCommand(t *testing.T) {
	fence, err := computer.RestoreFence(computer.FenceSnapshot{CoordinatorID: "coordinator-1", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	processor, err := nodelink.NewProcessor("executor-1", fence, nodelink.NewMemoryOperationLedger())
	if err != nil {
		t.Fatal(err)
	}
	command := commandEnvelope()
	command.Generation = 2
	applied := 0
	_, err = processor.Process(context.Background(), nodelink.ChannelIdentity{
		LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true,
	}, command, func(context.Context, nodelink.Envelope) error { applied++; return nil })
	if !errors.Is(err, computer.ErrFutureGeneration) {
		t.Fatalf("future command error = %v, want ErrFutureGeneration", err)
	}
	if got := fence.Snapshot(); got.CoordinatorID != "coordinator-1" || got.Generation != 1 {
		t.Fatalf("ordinary command advanced fence to %#v", got)
	}
	if applied != 0 {
		t.Fatalf("future command applied %d times", applied)
	}
}

func TestProcessorSerializesConcurrentDuplicateOperation(t *testing.T) {
	processor := mustProcessor(t, "executor-1")
	identity := nodelink.ChannelIdentity{LocalComputerID: "executor-1", PeerComputerID: "coordinator-1", ExecutorComputerID: "executor-1", MutuallyAuthenticated: true}
	var applied atomic.Int32
	var duplicates atomic.Int32
	start := make(chan struct{})
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := processor.Process(context.Background(), identity, commandEnvelope(), func(context.Context, nodelink.Envelope) error {
				applied.Add(1)
				return nil
			})
			if err != nil {
				t.Errorf("Process() error = %v", err)
				return
			}
			if result.Duplicate {
				duplicates.Add(1)
			}
		}()
	}
	close(start)
	group.Wait()
	if got := applied.Load(); got != 1 {
		t.Fatalf("operation applied %d times, want once", got)
	}
	if got := duplicates.Load(); got != 11 {
		t.Fatalf("duplicates = %d, want 11", got)
	}
}

func mustProcessor(t *testing.T, localID string) *nodelink.Processor {
	t.Helper()
	fence, err := computer.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	if err := fence.Accept(computer.CoordinatorTerm{CoordinatorID: "coordinator-1", Generation: 1}); err != nil {
		t.Fatal(err)
	}
	processor, err := nodelink.NewProcessor(domain.ComputerID(localID), fence, nodelink.NewMemoryOperationLedger())
	if err != nil {
		t.Fatal(err)
	}
	return processor
}

func commandEnvelope() nodelink.Envelope {
	return nodelink.Envelope{Version: nodelink.ProtocolVersion, Kind: nodelink.KindCommand, OperationID: "operation-1", Generation: 1, CoordinatorID: "coordinator-1", SourceComputerID: "coordinator-1", TargetComputerID: "executor-1", Payload: []byte(`{"text":"hello"}`)}
}

func withSource(envelope nodelink.Envelope, source string) nodelink.Envelope {
	envelope.SourceComputerID = domain.ComputerID(source)
	return envelope
}

func withVersion(envelope nodelink.Envelope, version uint16) nodelink.Envelope {
	envelope.Version = version
	return envelope
}
