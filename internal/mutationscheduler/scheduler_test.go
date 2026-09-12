package mutationscheduler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

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
	now := time.Unix(1_700_000_000, 0).UTC()
	scheduler, err := Open(Options{
		Now:    func() time.Time { return now },
		Wait:   func(context.Context, time.Duration) error { return nil },
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
