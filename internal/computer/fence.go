package computer

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"bria/internal/domain"
)

var (
	ErrInvalidGeneration   = errors.New("invalid coordinator generation")
	ErrStaleGeneration     = errors.New("stale coordinator generation")
	ErrFutureGeneration    = errors.New("unaccepted coordinator generation")
	ErrCoordinatorConflict = errors.New("coordinator identity conflicts with accepted generation")
	ErrNoCoordinator       = errors.New("coordinator term is not established")
)

type CoordinatorGeneration uint64

// CoordinatorTerm identifies the one manually appointed coordinator epoch.
type CoordinatorTerm struct {
	CoordinatorID domain.ComputerID
	Generation    CoordinatorGeneration
}

type FenceSnapshot CoordinatorTerm

// Fence stores the latest term accepted through an explicit handoff. Ordinary
// commands only Validate this term; they can never appoint a coordinator.
type Fence struct {
	mu   sync.RWMutex
	term CoordinatorTerm
}

func NewFence() (*Fence, error) { return &Fence{}, nil }

func RestoreFence(snapshot FenceSnapshot) (*Fence, error) {
	term := CoordinatorTerm(snapshot)
	if term.Generation == 0 && strings.TrimSpace(string(term.CoordinatorID)) == "" {
		return NewFence()
	}
	if err := validateTerm(term); err != nil {
		return nil, err
	}
	return &Fence{term: term}, nil
}

// Accept is the composition seam for a verified manual handoff. Higher terms
// advance the fence; a repeated current term is idempotent.
func (fence *Fence) Accept(term CoordinatorTerm) error {
	if fence == nil {
		return ErrNoCoordinator
	}
	if err := validateTerm(term); err != nil {
		return err
	}
	fence.mu.Lock()
	defer fence.mu.Unlock()
	if fence.term.Generation == 0 || term.Generation > fence.term.Generation {
		fence.term = term
		return nil
	}
	if term.Generation < fence.term.Generation {
		return ErrStaleGeneration
	}
	if term.CoordinatorID != fence.term.CoordinatorID {
		return ErrCoordinatorConflict
	}
	return nil
}

// Validate checks a command against the already accepted term and never
// advances coordinator authority.
func (fence *Fence) Validate(term CoordinatorTerm) error {
	if fence == nil {
		return ErrNoCoordinator
	}
	if err := validateTerm(term); err != nil {
		return err
	}
	fence.mu.RLock()
	defer fence.mu.RUnlock()
	if fence.term.Generation == 0 {
		return ErrNoCoordinator
	}
	if term.Generation < fence.term.Generation {
		return ErrStaleGeneration
	}
	if term.Generation > fence.term.Generation {
		return ErrFutureGeneration
	}
	if term.CoordinatorID != fence.term.CoordinatorID {
		return ErrCoordinatorConflict
	}
	return nil
}

func (fence *Fence) Snapshot() FenceSnapshot {
	if fence == nil {
		return FenceSnapshot{}
	}
	fence.mu.RLock()
	defer fence.mu.RUnlock()
	return FenceSnapshot(fence.term)
}

func validateTerm(term CoordinatorTerm) error {
	if term.Generation == 0 {
		return ErrInvalidGeneration
	}
	if strings.TrimSpace(string(term.CoordinatorID)) == "" {
		return fmt.Errorf("%w: coordinator id is required", ErrInvalidGeneration)
	}
	return nil
}
