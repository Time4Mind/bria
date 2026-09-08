package durableflow_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/durableflow"
	"bria/internal/messagejournal"
)

type acceptanceFaultJournal struct {
	*messagejournal.Journal
	fault     error
	remaining atomic.Int32
	ambiguous bool
}

type acceptanceReadFaultJournal struct {
	*messagejournal.Journal
	fault     error
	remaining atomic.Int32
}

func (j *acceptanceReadFaultJournal) Inputs(ctx context.Context, sessionID string) ([]messagejournal.Input, error) {
	if j.remaining.Add(-1) >= 0 {
		return nil, j.fault
	}
	return j.Journal.Inputs(ctx, sessionID)
}

func (j *acceptanceFaultJournal) MarkInputAccepted(ctx context.Context, session, message, owner string) (messagejournal.Input, error) {
	if j.remaining.Add(-1) >= 0 {
		if j.ambiguous {
			if _, err := j.Journal.MarkInputAccepted(ctx, session, message, owner); err != nil {
				return messagejournal.Input{}, err
			}
		}
		return messagejournal.Input{}, j.fault
	}
	return j.Journal.MarkInputAccepted(ctx, session, message, owner)
}

func TestExactObservedAcceptanceRetriesOnlyJournalCommit(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_write", true: "after_write"}[ambiguous], func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			j := &acceptanceFaultJournal{Journal: openJournal(t, path), fault: errors.New("synthetic acceptance save failure"), ambiguous: ambiguous}
			j.remaining.Store(1)
			f := newFlow(t, j, nil, nil, time.Unix(10, 0))
			for _, id := range []string{"a", "b"} {
				if _, err := f.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
					t.Fatal(err)
				}
			}
			var providerRequests int
			processor := inputProcessorFunc(func(ctx context.Context, in durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
				providerRequests++
				err := cb.OnAccepted(ctx, handoffReceipt(in, durableflow.HandoffAccepted))
				return durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessAccepted}, err
			})
			result, err := f.ProcessNextInput(ctx, "s", processor)
			if err != nil || result.State != durableflow.InputProcessAccepted {
				t.Fatalf("exact ACK lost: %s %v", result.State, err)
			}
			if providerRequests != 1 {
				t.Fatal("acceptance persistence replayed provider")
			}
			reopened := openJournal(t, path)
			inputs, err := reopened.Inputs(ctx, "s")
			if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputAccepted || inputs[1].Phase != messagejournal.InputPending {
				t.Fatalf("ACK not durable: %#v %v", inputs, err)
			}
			ready, err := newFlow(t, reopened, nil, nil, time.Unix(100, 0)).RootInputReady(ctx, "s", "b", 2)
			if ready || err != nil {
				t.Fatalf("root bypassed acceptance: %v %v", ready, err)
			}
		})
	}
}

func TestPermanentAcceptanceWriteFailureNeverIntentionallyDowngrades(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := &acceptanceFaultJournal{Journal: openJournal(t, path), fault: errors.New("synthetic persistent acceptance save failure")}
	j.remaining.Store(100)
	f := newFlow(t, j, nil, nil, time.Unix(10, 0))
	if _, err := f.EnqueueInput(ctx, "s", "a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	processor := inputProcessorFunc(func(ctx context.Context, in durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		_ = cb.OnAccepted(ctx, handoffReceipt(in, durableflow.HandoffAccepted))
		return durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessAccepted}, nil
	})
	result, err := f.ProcessNextInput(ctx, "s", processor)
	if !errors.Is(err, j.fault) || result.State != durableflow.InputProcessAccepted {
		t.Fatalf("observed ACK lost: %s %v", result.State, err)
	}
	inputs, err := openJournal(t, path).Inputs(ctx, "s")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputPending || inputs[0].Lease.Owner == "" {
		t.Fatalf("failed write intentionally downgraded/requeued: %#v %v", inputs, err)
	}
}

func TestRecoveryIncludesExpiredLeasedPendingWithoutClaimingAcceptance(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := openJournal(t, path)
	if _, _, err := j.EnqueueInput(ctx, "s", "a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.LeaseNextInput(ctx, "s", "old-worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.EnqueueInput(ctx, "s", "b", []byte("b")); err != nil {
		t.Fatal(err)
	}
	f := newFlow(t, openJournal(t, path), nil, nil, time.Unix(999, 0))
	var seen []string
	resolver := acceptedResolverFunc(func(_ context.Context, in durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
		seen = append(seen, in.MessageID)
		return durableflow.AcceptedResolutionResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, Resolution: durableflow.AcceptedUnknown}, nil
	})
	results, err := f.ReconcileAcceptedInputs(ctx, "s", resolver)
	if err != nil || len(results) != 1 || len(seen) != 1 || seen[0] != "a" || results[0].Resolution != durableflow.AcceptedUnknown {
		t.Fatalf("leased ambiguous attempt disappeared at bootstrap: %#v seen=%v err=%v", results, seen, err)
	}
	inputs, err := openJournal(t, path).Inputs(ctx, "s")
	if err != nil || len(inputs) != 2 || inputs[0].Phase != messagejournal.InputPending || inputs[0].Lease.Owner != "old-worker" || inputs[1].Lease.Owner != "" {
		t.Fatalf("bootstrap forged acceptance or requeued: %#v %v", inputs, err)
	}
}

func TestHandoffExactAcceptanceWriteFailureKeepsObservedAcceptance(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := &acceptanceFaultJournal{Journal: openJournal(t, path), fault: errors.New("synthetic acceptance save failure")}
	j.remaining.Store(100)
	p := inputProviderFunc(func(_ context.Context, in durableflow.ProviderInput) (durableflow.HandoffResult, error) {
		return handoffReceipt(in, durableflow.HandoffAccepted), nil
	})
	f := newFlow(t, j, p, nil, time.Unix(10, 0))
	if _, err := f.EnqueueInput(ctx, "s", "a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	result, err := f.DispatchNextInput(ctx, "s")
	if result.State != durableflow.HandoffAccepted || !errors.Is(err, j.fault) {
		t.Fatalf("exact handoff ACK lost: %#v %v", result, err)
	}
	inputs, err := openJournal(t, path).Inputs(ctx, "s")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputPending || inputs[0].Lease.Owner == "" {
		t.Fatalf("handoff intentionally downgraded/requeued: %#v %v", inputs, err)
	}
}

func TestLeasedPendingRecoveryRequiresExactNativeProof(t *testing.T) {
	for _, mode := range []string{"missing", "error", "wrong_tuple", "unknown_with_proof", "accepted", "completed", "terminal_failed", "proof_save_failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			j := openJournal(t, path)
			for _, id := range []string{"a", "b"} {
				if _, _, err := j.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := j.LeaseNextInput(ctx, "s", "prior-worker", time.Unix(10, 0), time.Minute); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("synthetic proof/save fault")
			var journal durableflow.Journal = openJournal(t, path)
			if mode == "proof_save_failure" {
				failing := &acceptanceFaultJournal{Journal: j, fault: fault}
				failing.remaining.Store(100)
				journal = failing
			}
			f := newFlow(t, journal, nil, nil, time.Unix(999, 0))
			resolver := acceptedResolverFunc(func(_ context.Context, in durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
				if !in.PreviouslyUnaccepted || in.PreviouslyUnknown || in.PreviouslyFailed {
					t.Fatal("leased pending proof context lost")
				}
				out := durableflow.AcceptedResolutionResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, Resolution: durableflow.AcceptedPending, AcceptanceProven: true}
				switch mode {
				case "missing":
					out.AcceptanceProven = false
				case "error":
					return out, fault
				case "wrong_tuple":
					out.Sequence++
				case "unknown_with_proof":
					out.Resolution = durableflow.AcceptedUnknown
				case "completed":
					out.Resolution = durableflow.AcceptedCompleted
				case "terminal_failed":
					out.Resolution = durableflow.AcceptedTerminalFailed
				}
				return out, nil
			})
			results, err := f.ReconcileAcceptedInputs(ctx, "s", resolver)
			wantPhase, wantResult := messagejournal.InputPending, durableflow.AcceptedUnknown
			switch mode {
			case "accepted":
				wantPhase, wantResult = messagejournal.InputAccepted, durableflow.AcceptedPending
			case "completed":
				wantPhase, wantResult = messagejournal.InputCompleted, durableflow.AcceptedCompleted
			case "terminal_failed":
				wantPhase, wantResult = messagejournal.InputTerminalFailed, durableflow.AcceptedTerminalFailed
			case "proof_save_failure":
				wantResult = durableflow.AcceptedPending
			}
			wantError := mode == "missing" || mode == "error" || mode == "wrong_tuple" || mode == "proof_save_failure"
			if (err != nil) != wantError || len(results) != 1 || results[0].Resolution != wantResult {
				t.Fatalf("proof resolution: %#v %v", results, err)
			}
			inputs, err := openJournal(t, path).Inputs(ctx, "s")
			if err != nil || len(inputs) != 2 || inputs[0].Phase != wantPhase || inputs[1].Phase != messagejournal.InputPending {
				t.Fatalf("physical proof custody: %#v %v", inputs, err)
			}
			if wantPhase == messagejournal.InputPending && inputs[0].Lease.Owner != "prior-worker" {
				t.Fatal("unproved lease released")
			}
			ready, err := f.RootInputReady(ctx, "s", "b", 2)
			if err != nil || ready != (mode == "completed" || mode == "terminal_failed") {
				t.Fatalf("wrong successor admission: %v %v", ready, err)
			}
		})
	}
}

func TestUncommittedObservedACKFencesWakeWithoutErasingDurableLease(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := &acceptanceFaultJournal{Journal: openJournal(t, path), fault: errors.New("synthetic persistent acceptance save failure")}
	j.remaining.Store(100)
	now := time.Unix(10, 0)
	f, err := durableflow.New(j, nil, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		if _, err := f.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	p := inputProcessorFunc(func(ctx context.Context, in durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		err := cb.OnAccepted(ctx, handoffReceipt(in, durableflow.HandoffAccepted))
		return durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessAccepted}, err
	})
	if _, err := f.ProcessNextInput(ctx, "s", p); !errors.Is(err, j.fault) {
		t.Fatal(err)
	}
	before, err := j.Inputs(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	now = time.Unix(999, 0)
	var sent int
	p = inputProcessorFunc(func(_ context.Context, in durableflow.ProviderInput, _ durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		sent++
		return durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessDeferred}, nil
	})
	for i := 0; i < 2; i++ {
		if _, err := f.ProcessNextInput(ctx, "s", p); err == nil {
			t.Error("wake passed unresolved observed ACK")
		}
	}
	if sent != 0 {
		t.Fatalf("expired accepted attempt replayed %d times", sent)
	}
	after, err := openJournal(t, path).Inputs(ctx, "s")
	if err != nil || len(after) != 2 || after[0].Phase != messagejournal.InputPending || after[0].Lease != before[0].Lease {
		t.Fatalf("wake erased ambiguity marker: %#v %v", after, err)
	}
	reopened := newFlow(t, openJournal(t, path), nil, nil, now)
	results, err := reopened.ReconcileAcceptedInputs(ctx, "s", acceptedResolverFunc(func(_ context.Context, in durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
		if !in.PreviouslyUnaccepted {
			t.Fatal("lost pending-attempt marker")
		}
		return durableflow.AcceptedResolutionResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, Resolution: durableflow.AcceptedUnknown}, nil
	}))
	if err != nil || len(results) != 1 || results[0].Resolution != durableflow.AcceptedUnknown {
		t.Fatalf("bootstrap candidate lost: %#v %v", results, err)
	}
	if _, err := j.Journal.MarkInputAccepted(ctx, "s", "a", "worker"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Journal.ResolveAcceptedInput(ctx, "s", "a", 1, messagejournal.InputCompleted); err != nil {
		t.Fatal(err)
	}
	result, err := f.ProcessNextInput(ctx, "s", p)
	if err != nil || result.MessageID != "b" || sent != 1 {
		t.Fatalf("durable terminal did not release only successor: %#v sent=%d %v", result, sent, err)
	}
}

func TestObservedACKReadFailureFencesWakeAfterReadsRecover(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_%v", legacy), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "journal.json")
			j := &acceptanceReadFaultJournal{Journal: openJournal(t, path), fault: errors.New("synthetic acceptance lookup failure")}
			now := time.Unix(10, 0)
			var requests int
			provider := inputProviderFunc(func(_ context.Context, in durableflow.ProviderInput) (durableflow.HandoffResult, error) {
				requests++
				j.remaining.Store(1)
				return handoffReceipt(in, durableflow.HandoffAccepted), nil
			})
			f, err := durableflow.New(j, provider, nil, durableflow.Options{Owner: "worker", LeaseDuration: time.Minute, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.EnqueueInput(ctx, "s", "a", []byte("a")); err != nil {
				t.Fatal(err)
			}
			processor := inputProcessorFunc(func(ctx context.Context, in durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
				requests++
				j.remaining.Store(1)
				err := cb.OnAccepted(ctx, handoffReceipt(in, durableflow.HandoffAccepted))
				return durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessAccepted}, err
			})
			if legacy {
				_, err = f.DispatchNextInput(ctx, "s")
			} else {
				_, err = f.ProcessNextInput(ctx, "s", processor)
			}
			if !errors.Is(err, j.fault) {
				t.Fatalf("missing acceptance read error: %v", err)
			}
			before, err := j.Inputs(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			now = time.Unix(999, 0)
			if legacy {
				_, err = f.DispatchNextInput(ctx, "s")
			} else {
				_, err = f.ProcessNextInput(ctx, "s", processor)
			}
			if err == nil || requests != 1 {
				t.Fatalf("read-failed ACK replayed: requests=%d err=%v", requests, err)
			}
			after, err := openJournal(t, path).Inputs(ctx, "s")
			if err != nil || len(after) != 1 || after[0].Phase != messagejournal.InputPending || after[0].Lease != before[0].Lease {
				t.Fatalf("read-failed ACK marker lost: %#v %v", after, err)
			}
		})
	}
}
