package computer_test

import (
	"errors"
	"testing"

	"bria/internal/computer"
	"bria/internal/domain"
)

func TestCoordinatorFenceRejectsStaleAndConflictingGeneration(t *testing.T) {
	fence, err := computer.RestoreFence(computer.FenceSnapshot{
		CoordinatorID: domain.ComputerID("coordinator-new"),
		Generation:    3,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := fence.Accept(computer.CoordinatorTerm{CoordinatorID: "coordinator-old", Generation: 2}); !errors.Is(err, computer.ErrStaleGeneration) {
		t.Fatalf("stale Accept error = %v, want ErrStaleGeneration", err)
	}
	if err := fence.Accept(computer.CoordinatorTerm{CoordinatorID: "coordinator-other", Generation: 3}); !errors.Is(err, computer.ErrCoordinatorConflict) {
		t.Fatalf("conflicting Accept error = %v, want ErrCoordinatorConflict", err)
	}
	if got := fence.Snapshot(); got.CoordinatorID != "coordinator-new" || got.Generation != 3 {
		t.Fatalf("fence changed after rejection: %#v", got)
	}
}

func TestCoordinatorFenceAdvancesAndAcceptsCurrentTerm(t *testing.T) {
	fence, err := computer.NewFence()
	if err != nil {
		t.Fatal(err)
	}
	term := computer.CoordinatorTerm{CoordinatorID: "coordinator-1", Generation: 1}
	if err := fence.Accept(term); err != nil {
		t.Fatal(err)
	}
	if err := fence.Accept(term); err != nil {
		t.Fatalf("same accepted term must be idempotent: %v", err)
	}
	if err := fence.Accept(computer.CoordinatorTerm{CoordinatorID: "coordinator-2", Generation: 2}); err != nil {
		t.Fatal(err)
	}
	if got := fence.Snapshot(); got.CoordinatorID != "coordinator-2" || got.Generation != 2 {
		t.Fatalf("snapshot = %#v", got)
	}
}
