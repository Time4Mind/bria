package sessionexpiry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"bria/internal/domain"
)

type SessionSource interface {
	List(context.Context) ([]domain.Session, error)
}

type SessionCloser interface {
	Close(context.Context, domain.SessionID) error
}

type Result struct {
	Examined int
	Closed   []domain.SessionID
	Failed   []domain.SessionID
}

type Scheduler struct {
	source SessionSource
	closer SessionCloser
	now    func() time.Time
}

type ErrorReporter func(error)

func New(source SessionSource, closer SessionCloser, now func() time.Time) (*Scheduler, error) {
	if source == nil || closer == nil || now == nil {
		return nil, errors.New("session expiry dependencies are required")
	}
	return &Scheduler{source: source, closer: closer, now: now}, nil
}

func (scheduler *Scheduler) Sweep(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("session expiry context is required")
	}
	sessions, err := scheduler.source.List(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list sessions for expiry: %w", err)
	}
	sort.Slice(sessions, func(left, right int) bool { return sessions[left].ID() < sessions[right].ID() })
	result := Result{Examined: len(sessions)}
	var failures []error
	now := scheduler.now().UTC()
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if !session.Expired(now) || !closableStatus(session.Status()) {
			continue
		}
		if err := scheduler.closer.Close(ctx, session.ID()); err != nil {
			result.Failed = append(result.Failed, session.ID())
			failures = append(failures, fmt.Errorf("close expired session %q: %w", session.ID(), err))
			continue
		}
		result.Closed = append(result.Closed, session.ID())
	}
	return result, errors.Join(failures...)
}

// Run performs an immediate startup sweep and then repeats it. With a reporter,
// an individual close failure is observable but does not permanently disable
// expiry processing; without one the error is returned to the supervisor.
func (scheduler *Scheduler) Run(ctx context.Context, interval time.Duration, report ErrorReporter) error {
	if ctx == nil {
		return errors.New("session expiry context is required")
	}
	if interval <= 0 {
		return errors.New("session expiry interval must be positive")
	}
	runSweep := func() error {
		_, err := scheduler.Sweep(ctx)
		if err == nil {
			return nil
		}
		if report == nil {
			return err
		}
		report(err)
		return nil
	}
	if err := runSweep(); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := runSweep(); err != nil {
				return err
			}
		}
	}
}

func closableStatus(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionReady, domain.SessionRunning, domain.SessionStopping:
		return true
	default:
		return false
	}
}
