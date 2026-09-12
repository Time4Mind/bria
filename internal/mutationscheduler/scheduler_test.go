package mutationscheduler

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRoutineWaitsForActiveInteractiveLease(t *testing.T) {
	t.Parallel()

	var nowNanos atomic.Int64
	nowNanos.Store(time.Unix(1_700_000_000, 0).UTC().UnixNano())
	scheduler, err := Open(Options{
		Now: func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() },
		Wait: func(_ context.Context, delay time.Duration) error {
			nowNanos.Add(int64(delay))
			return nil
		},
		Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	interactive := acquireInteractive(t, scheduler)
	routineResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(context.Background(), Mutation{
			Method: "editMessageText", ChatID: 42, CardID: 8, Priority: Routine,
		})
		routineResult <- acquireResult{lease: lease, err: acquireErr}
	}()

	select {
	case result := <-routineResult:
		if result.lease != nil {
			_ = result.lease.Complete(Outcome{})
		}
		t.Fatalf("routine acquired while interactive lease was active: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := interactive.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-routineResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if elapsed := time.Duration(nowNanos.Load() - time.Unix(1_700_000_000, 0).UTC().UnixNano()); elapsed < globalStartInterval {
			t.Fatalf("routine bypassed global rate limit after wake: elapsed %s", elapsed)
		}
		if err := result.lease.Complete(Outcome{}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("routine did not wake after interactive lease completed")
	}
}

func TestInteractiveDoesNotWaitForHeavyRoutineWaiter(t *testing.T) {
	var nowCalls atomic.Int64
	var nowNanos atomic.Int64
	nowNanos.Store(time.Unix(1_700_000_000, 0).UTC().UnixNano())
	routineQueued := make(chan struct{})
	scheduler, err := Open(Options{
		Now: func() time.Time {
			if nowCalls.Add(1) == 3 {
				close(routineQueued)
			}
			return time.Unix(0, nowNanos.Load()).UTC()
		},
		Wait: func(_ context.Context, delay time.Duration) error {
			nowNanos.Add(int64(delay))
			return nil
		},
		Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	active := acquireInteractive(t, scheduler)
	routineCtx, cancelRoutine := context.WithCancel(context.Background())
	defer cancelRoutine()
	routineResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(routineCtx, Mutation{
			Method: "editMessageText", ChatID: 42, CardID: 8, Priority: Routine, Heavy: true,
		})
		routineResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	select {
	case <-routineQueued:
	case <-time.After(time.Second):
		t.Fatal("routine did not enter the scheduler")
	}
	// Let the routine reach its blocked scheduling point before the second
	// interactive request arrives.
	time.Sleep(20 * time.Millisecond)
	interactiveResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(context.Background(), Mutation{
			Method: "editMessageText", ChatID: 42, CardID: 9, Priority: Interactive, Heavy: true,
		})
		interactiveResult <- acquireResult{lease: lease, err: acquireErr}
	}()

	select {
	case result := <-interactiveResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.lease.Complete(Outcome{}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		cancelRoutine()
		_ = active.Complete(Outcome{})
		t.Fatal("interactive mutation waited for a blocked heavy routine")
	}
	select {
	case result := <-routineResult:
		if result.lease != nil {
			_ = result.lease.Complete(Outcome{})
		}
		t.Fatalf("routine acquired before active interactive completed: %v", result.err)
	default:
	}
	if err := active.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-routineResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.lease.Complete(Outcome{}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("heavy routine did not wake after interactive completion")
	}
}

func TestHeavyMutationsRemainSerialized(t *testing.T) {
	scheduler, _ := newCountingScheduler(t)
	first, err := scheduler.Acquire(context.Background(), Mutation{
		Method: "sendDocument", ChatID: 42, Priority: Interactive, Heavy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(context.Background(), Mutation{
			Method: "sendDocument", ChatID: 42, Priority: Interactive, Heavy: true,
		})
		secondResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	select {
	case result := <-secondResult:
		if result.lease != nil {
			_ = result.lease.Complete(Outcome{})
		}
		t.Fatalf("second heavy mutation acquired before first completed: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := first.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.lease.Complete(Outcome{}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second heavy mutation did not wake after first completed")
	}
}

func TestCanceledRoutineWaiterDoesNotBlockLaterMutation(t *testing.T) {
	scheduler, _ := newCountingScheduler(t)
	active := acquireInteractive(t, scheduler)
	routineCtx, cancelRoutine := context.WithCancel(context.Background())
	routineResult := make(chan error, 1)
	go func() {
		_, acquireErr := scheduler.Acquire(routineCtx, Mutation{
			Method: "editMessageText", ChatID: 42, CardID: 8, Priority: Routine,
		})
		routineResult <- acquireErr
	}()
	time.Sleep(20 * time.Millisecond)
	cancelRoutine()
	select {
	case err := <-routineResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled routine returned %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled routine waiter did not return")
	}
	if err := active.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	later, err := scheduler.Acquire(context.Background(), Mutation{
		Method: "editMessageText", ChatID: 42, CardID: 9, Priority: Routine,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := later.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedRoutineKeepsCardSupersession(t *testing.T) {
	scheduler, _ := newCountingScheduler(t)
	active := acquireInteractive(t, scheduler)
	firstResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(context.Background(), Mutation{
			Method: "editMessageText", ChatID: 42, CardID: 8, Priority: Routine,
		})
		firstResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	waitForCardVersion(t, scheduler, "42:8", 1)
	secondResult := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := scheduler.Acquire(context.Background(), Mutation{
			Method: "editMessageText", ChatID: 42, CardID: 8, Priority: Routine,
		})
		secondResult <- acquireResult{lease: lease, err: acquireErr}
	}()
	waitForCardVersion(t, scheduler, "42:8", 2)
	select {
	case result := <-firstResult:
		if !errors.Is(result.err, ErrSuperseded) {
			t.Fatalf("first routine returned %v, want superseded", result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("superseded routine did not return")
	}
	if err := active.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if err := result.lease.Complete(Outcome{}); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("latest routine did not wake after interactive completion")
	}
}

func waitForCardVersion(t *testing.T, scheduler *MutationScheduler, cardKey string, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		version := scheduler.cardVersions[cardKey]
		scheduler.mu.Unlock()
		if version == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("card version did not reach %d", want)
}

type acquireResult struct {
	lease *Lease
	err   error
}

func TestSuccessfulCompleteSkipsUnchangedPersistedState(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome Outcome
	}{
		{name: "implicit-success", outcome: Outcome{}},
		{name: "explicit-200", outcome: Outcome{HTTPStatus: http.StatusOK}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scheduler, writes := newCountingScheduler(t)
			lease := acquireInteractive(t, scheduler)
			if got := *writes; got != 1 {
				t.Fatalf("writes after acquire = %d, want 1", got)
			}
			notify := scheduler.notify
			if err := lease.Complete(test.outcome); err != nil {
				t.Fatal(err)
			}
			if got := *writes; got != 1 {
				t.Fatalf("writes after successful complete = %d, want unchanged 1", got)
			}
			select {
			case <-notify:
			default:
				t.Fatal("successful complete did not preserve scheduler broadcast")
			}
		})
	}
}

func TestUnsuccessfulCompleteStillPersists(t *testing.T) {
	tests := []struct {
		name    string
		outcome Outcome
	}{
		{name: "transport-error", outcome: Outcome{Err: errors.New("transport failed")}},
		{name: "rate-limited", outcome: Outcome{HTTPStatus: http.StatusTooManyRequests, ErrorCode: 429}},
		{name: "server-error", outcome: Outcome{HTTPStatus: http.StatusBadGateway, ErrorCode: 502}},
		{name: "unauthorized", outcome: Outcome{HTTPStatus: http.StatusUnauthorized, ErrorCode: 401}},
		{name: "recipient-forbidden", outcome: Outcome{HTTPStatus: http.StatusForbidden, ErrorCode: 403}},
		{name: "terminal-api-error", outcome: Outcome{HTTPStatus: http.StatusBadRequest, ErrorCode: 400}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scheduler, writes := newCountingScheduler(t)
			lease := acquireInteractive(t, scheduler)
			if err := lease.Complete(test.outcome); err != nil {
				t.Fatal(err)
			}
			if got := *writes; got != 2 {
				t.Fatalf("writes after unsuccessful complete = %d, want 2", got)
			}
		})
	}
}

func TestSuccessfulRecoveryCompletePersistsChangedState(t *testing.T) {
	t.Parallel()

	scheduler, writes := newCountingScheduler(t)
	scheduler.state.ServerFailures = 2
	scheduler.state.ProbeRequired = true
	scheduler.state.CooldownUntil = time.Unix(1_699_999_999, 0).UTC()
	scheduler.state.CooldownCause = "transport_error"
	scheduler.epoch = 1

	lease, err := scheduler.Acquire(context.Background(), Mutation{Method: "sendMessage", ChatID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	if got := *writes; got != 2 {
		t.Fatalf("writes after recovery complete = %d, want 2", got)
	}
	if scheduler.state.ServerFailures != 0 || scheduler.state.ProbeRequired ||
		!scheduler.state.CooldownUntil.IsZero() || scheduler.state.CooldownCause != "" {
		t.Fatalf("recovery state was not cleared: %+v", scheduler.state)
	}
}

func TestEarlierSuccessDoesNotEraseConcurrentRateLimit(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	scheduler, err := Open(Options{
		Now: func() time.Time { return now },
		Wait: func(_ context.Context, delay time.Duration) error {
			now = now.Add(delay)
			return nil
		},
		Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	scheduler.persist = func() error {
		writes++
		return nil
	}
	mutation := Mutation{Method: "sendMessage", ChatID: 42}
	earlier, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	later, err := scheduler.Acquire(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	if err := later.Complete(Outcome{HTTPStatus: http.StatusTooManyRequests, ErrorCode: 429}); err != nil {
		t.Fatal(err)
	}
	if err := earlier.Complete(Outcome{}); err != nil {
		t.Fatal(err)
	}
	if !scheduler.state.ProbeRequired || scheduler.state.CooldownCause != "rate_limited" ||
		!scheduler.state.CooldownUntil.After(now) {
		t.Fatalf("earlier success erased concurrent rate limit: %+v", scheduler.state)
	}
	if writes != 4 {
		t.Fatalf("writes with concurrent state transition = %d, want 4", writes)
	}
}

func newCountingScheduler(t *testing.T) (*MutationScheduler, *int) {
	t.Helper()
	var nowNanos atomic.Int64
	nowNanos.Store(time.Unix(1_700_000_000, 0).UTC().UnixNano())
	scheduler, err := Open(Options{
		Now: func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() },
		Wait: func(_ context.Context, delay time.Duration) error {
			nowNanos.Add(int64(delay))
			return nil
		},
		Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	scheduler.persist = func() error {
		writes++
		return nil
	}
	return scheduler, &writes
}

func acquireInteractive(t *testing.T, scheduler *MutationScheduler) *Lease {
	t.Helper()
	lease, err := scheduler.Acquire(context.Background(), Mutation{
		Method: "editMessageText", ChatID: 42, CardID: 7, Priority: Interactive,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
