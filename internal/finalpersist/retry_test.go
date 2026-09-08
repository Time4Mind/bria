package finalpersist_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"bria/internal/finalpersist"
)

func quickPolicy() finalpersist.Policy {
	return finalpersist.Policy{InitialDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond}
}

func TestRetryDefaultPolicyAndPacedExponentialCap(t *testing.T) {
	if got := finalpersist.DefaultPolicy(); got.InitialDelay != time.Second || got.MaxDelay != 30*time.Second {
		t.Fatalf("default policy=%+v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fault := errors.New("synthetic storage fault")
	var attempts []time.Time
	var failures []uint64
	err := finalpersist.Retry(ctx, quickPolicy(), func(context.Context) error {
		attempts = append(attempts, time.Now())
		if len(attempts) < 6 {
			return fault
		}
		return nil
	}, func(n uint64, err error) {
		failures = append(failures, n)
		if err != fault {
			t.Error("observer lost the original failure")
		}
	})
	if err != nil || len(attempts) != 6 || !reflect.DeepEqual(failures, []uint64{1, 2, 3, 4, 5}) {
		t.Fatalf("retry result=%v attempts=%d failures=%v", err, len(attempts), failures)
	}
	for i, pause := range []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond} {
		if elapsed := attempts[i+1].Sub(attempts[i]); elapsed < pause {
			t.Fatalf("retry pause[%d]=%v shorter than %v", i, elapsed, pause)
		}
	}
}

func TestRetryFirstSavePreservesDrainAndContextValues(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "marker"))
	cancel()
	calls := 0
	err := finalpersist.Retry(ctx, quickPolicy(), func(saveCtx context.Context) error {
		calls++
		if saveCtx.Err() != nil || saveCtx.Done() != nil || saveCtx.Value(key{}) != "marker" {
			t.Error("first save did not preserve uncancelled drain with context values")
		}
		return nil
	}, nil)
	if err != nil || calls != 1 {
		t.Fatalf("first successful drain=%v calls=%d", err, calls)
	}
}

func TestRetryShutdownAfterFirstFailureDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls, failures := 0, uint64(0)
	err := finalpersist.Retry(ctx, finalpersist.DefaultPolicy(), func(context.Context) error {
		calls++
		cancel()
		return errors.New("synthetic failed drain")
	}, func(n uint64, _ error) { failures = n })
	if !errors.Is(err, context.Canceled) || calls != 1 || failures != 1 {
		t.Fatalf("shutdown=%v calls=%d failures=%d", err, calls, failures)
	}
}

func TestRetrySubsequentSaveIsCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		calls := 0
		result <- finalpersist.Retry(ctx, quickPolicy(), func(saveCtx context.Context) error {
			calls++
			if calls == 1 {
				return errors.New("synthetic first failure")
			}
			close(entered)
			<-saveCtx.Done()
			return saveCtx.Err()
		}, nil)
	}()
	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("retry returned without second attempt: %v", err)
	case <-time.After(time.Second):
		t.Fatal("retry did not reach the second attempt")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled retry=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("retry save ignored shutdown context")
	}
}

func TestRetryPersistentFailureStopsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failure := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		result <- finalpersist.Retry(ctx, finalpersist.Policy{InitialDelay: time.Hour, MaxDelay: time.Hour},
			func(context.Context) error { return errors.New("synthetic persistent failure") },
			func(uint64, error) { failure <- struct{}{} })
	}()
	select {
	case <-failure:
	case <-time.After(time.Second):
		t.Fatal("first failure was not observed")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled backoff=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("backoff delayed shutdown")
	}
}

func TestRetrySupersededBindingNeverRetries(t *testing.T) {
	errSuperseded := errors.Join(errors.New("synthetic guard"), finalpersist.ErrSuperseded)
	calls, failures := 0, uint64(0)
	err := finalpersist.Retry(context.Background(), quickPolicy(), func(context.Context) error {
		calls++
		return errSuperseded
	}, func(n uint64, _ error) { failures = n })
	if !errors.Is(err, finalpersist.ErrSuperseded) || calls != 1 || failures != 0 {
		t.Fatalf("superseded=%v calls=%d failures=%d", err, calls, failures)
	}
}

func TestRetryRejectsInvalidPolicyBeforeSave(t *testing.T) {
	for _, policy := range []finalpersist.Policy{{}, {InitialDelay: -1, MaxDelay: time.Second}, {InitialDelay: time.Second}, {InitialDelay: 2, MaxDelay: 1}} {
		calls := 0
		err := finalpersist.Retry(context.Background(), policy, func(context.Context) error { calls++; return nil }, nil)
		if err == nil || calls != 0 {
			t.Fatalf("invalid policy=%+v result=%v calls=%d", policy, err, calls)
		}
	}
}

func TestRetryAmbiguousWriteUsesSameIdempotentSave(t *testing.T) {
	// The caller owns exact identity and idempotence. Retry only repeats the
	// supplied final-write closure; it has no provider or message-submit port.
	durable := map[string]string{}
	calls := 0
	err := finalpersist.Retry(context.Background(), quickPolicy(), func(context.Context) error {
		calls++
		if prior, exists := durable["exact-message"]; exists && prior != "exact-final" {
			return finalpersist.ErrSuperseded
		}
		durable["exact-message"] = "exact-final"
		if calls == 1 {
			return errors.New("synthetic write committed but acknowledgement lost")
		}
		return nil
	}, nil)
	if err != nil || calls != 2 || len(durable) != 1 || durable["exact-message"] != "exact-final" {
		t.Fatalf("ambiguous save result=%v attempts=%d exact_final_count=%d", err, calls, len(durable))
	}
}

func TestRetryPersistentErrorsHaveNoAttemptLimitBeforeShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var observed uint64
	err := finalpersist.Retry(ctx, quickPolicy(), func(context.Context) error {
		calls++
		return errors.New("synthetic persistent fault")
	}, func(n uint64, _ error) {
		observed = n
		if n == 12 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || calls != 12 || observed != 12 {
		t.Fatalf("persistent retry=%v attempts=%d observed=%d", err, calls, observed)
	}
}

func TestRetryStopsOnLaterSupersededGuard(t *testing.T) {
	calls := 0
	var observed []uint64
	err := finalpersist.Retry(context.Background(), quickPolicy(), func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("synthetic initial write fault")
		}
		return finalpersist.ErrSuperseded
	}, func(n uint64, _ error) { observed = append(observed, n) })
	if !errors.Is(err, finalpersist.ErrSuperseded) || calls != 2 || !reflect.DeepEqual(observed, []uint64{1}) {
		t.Fatalf("late supersession=%v attempts=%d observed=%v", err, calls, observed)
	}
}

func TestRetryInvalidCallbacksAndExtremeValidPolicy(t *testing.T) {
	if err := finalpersist.Retry(nil, quickPolicy(), func(context.Context) error { t.Fatal("nil context reached save"); return nil }, nil); err == nil {
		t.Fatal("nil context accepted")
	}
	if err := finalpersist.Retry(context.Background(), quickPolicy(), nil, nil); err == nil {
		t.Fatal("nil save accepted")
	}
	maximum := time.Duration(1<<63 - 1)
	policy := finalpersist.Policy{InitialDelay: maximum/2 + 1, MaxDelay: maximum}
	if err := finalpersist.Retry(context.Background(), policy, func(context.Context) error { return nil }, nil); err != nil {
		t.Fatalf("valid extreme duration rejected: %v", err)
	}
}
