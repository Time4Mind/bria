package turnadmission_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/turnadmission"
)

func assertAdmissionPending(t *testing.T, a *turnadmission.Admission) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unsettled admission Wait=%v, want cancelled waiter", err)
	}
}

func waitAdmission(t *testing.T, a *turnadmission.Admission, want error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.Wait(ctx); !errors.Is(err, want) {
		t.Fatalf("settled admission Wait=%v, want %v", err, want)
	}
}

func TestAdmissionWaitsForSealRootAndEveryRegisteredSteer(t *testing.T) {
	for _, sealFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish-before-seal", true: "seal-before-finish"}[sealFirst], func(t *testing.T) {
			a := turnadmission.NewAdmission()
			assertAdmissionPending(t, a)
			one, ok := a.RegisterSteer()
			if !ok || one == nil {
				t.Fatal("steer registration rejected before seal")
			}
			two, ok := a.RegisterSteer()
			if !ok || two == nil {
				t.Fatal("second steer registration rejected before seal")
			}
			if sealFirst {
				a.Seal()
			}
			a.FinishRoot(nil)
			a.FinishRoot(nil)
			assertAdmissionPending(t, a)
			one.Finish(nil)
			one.Finish(nil)
			assertAdmissionPending(t, a)
			two.Finish(nil)
			if !sealFirst {
				assertAdmissionPending(t, a)
				a.Seal()
			}
			a.Seal()
			if ticket, ok := a.RegisterSteer(); ok || ticket != nil {
				t.Fatal("late steer registration succeeded after seal")
			}
			waitAdmission(t, a, nil)
			// Idempotent retries cannot replace an already committed result.
			a.FinishRoot(errors.New("late duplicate root"))
			one.Finish(errors.New("late duplicate steer"))
			waitAdmission(t, a, nil)
		})
	}
}

func TestAdmissionCommitFailureIsStickyButDoesNotSettleEarly(t *testing.T) {
	for _, rootFails := range []bool{false, true} {
		a := turnadmission.NewAdmission()
		ticket, _ := a.RegisterSteer()
		a.Seal()
		private := errors.New("synthetic private commit details")
		if rootFails {
			a.FinishRoot(private)
			a.FinishRoot(nil)
			assertAdmissionPending(t, a)
			ticket.Finish(nil)
		} else {
			ticket.Finish(private)
			ticket.Finish(nil)
			assertAdmissionPending(t, a)
			a.FinishRoot(nil)
		}
		waitAdmission(t, a, turnadmission.ErrCommitFailed)
		if err := a.Wait(context.Background()); err != turnadmission.ErrCommitFailed || errors.Is(err, private) {
			t.Fatal("admission exposed raw commit error instead of the closed sentinel")
		}
	}
}

func TestAdmissionCancellationDoesNotChangeSharedSettlement(t *testing.T) {
	a := turnadmission.NewAdmission()
	a.Seal()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- a.Wait(ctx) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter ignored cancellation")
	}
	assertAdmissionPending(t, a)
	a.FinishRoot(nil)
	waitAdmission(t, a, nil)
	if err := a.Wait(ctx); err != nil {
		t.Fatalf("published settlement lost to cancelled context: %v", err)
	}
}

func TestAdmissionMayRegisterAfterRootCommitUntilSeal(t *testing.T) {
	a := turnadmission.NewAdmission()
	a.FinishRoot(nil)
	assertAdmissionPending(t, a)
	ticket, ok := a.RegisterSteer()
	if !ok || ticket == nil {
		t.Fatal("root commit closed registration before Seal")
	}
	a.Seal()
	assertAdmissionPending(t, a)
	var finishes sync.WaitGroup
	for range 32 {
		finishes.Add(1)
		go func() {
			defer finishes.Done()
			ticket.Finish(errors.New("synthetic failed commit"))
		}()
	}
	finishes.Wait()
	waitAdmission(t, a, turnadmission.ErrCommitFailed)
}

func TestAdmissionRootOnlyAndInvalidWaitContext(t *testing.T) {
	a := turnadmission.NewAdmission()
	if err := a.Wait(nil); err == nil || errors.Is(err, turnadmission.ErrCommitFailed) {
		t.Fatal("nil context must not imply a commit outcome")
	}
	a.Seal()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := a.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired waiter=%v", err)
	}
	a.FinishRoot(nil)
	waitAdmission(t, a, nil)
}

func TestAdmissionConcurrentFinishReadersAndLateRegistration(t *testing.T) {
	a := turnadmission.NewAdmission()
	const count = 32
	tickets := make([]*turnadmission.Ticket, count)
	for i := range tickets {
		tickets[i], _ = a.RegisterSteer()
	}
	start := make(chan struct{})
	var writers, readers sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for i := range count {
		readers.Add(1)
		go func() {
			defer readers.Done()
			if err := a.Wait(ctx); err != turnadmission.ErrCommitFailed {
				t.Errorf("reader settlement=%v", err)
			}
		}()
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			<-start
			// Racing registrations must either be included or rejected by Seal.
			if late, ok := a.RegisterSteer(); ok {
				late.Finish(nil)
			}
			a.Seal()
			a.FinishRoot(nil)
			err := error(nil)
			if i == 0 {
				err = errors.New("synthetic failure")
			}
			tickets[i].Finish(err)
			tickets[i].Finish(nil)
		}(i)
	}
	close(start)
	writers.Wait()
	readers.Wait()
	if ticket, ok := a.RegisterSteer(); ok || ticket != nil {
		t.Fatal("settled admission accepted another steer")
	}
	waitAdmission(t, a, turnadmission.ErrCommitFailed)
}
