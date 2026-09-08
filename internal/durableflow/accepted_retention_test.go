package durableflow_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"bria/internal/durableflow"
	"bria/internal/messagejournal"
)

func rootInputReady(flow *durableflow.Flow, ctx context.Context, session, message string, sequence uint64) (bool, error) {
	return flow.RootInputReady(ctx, session, message, sequence)
}

func TestRootInputAdmissionRequiresEarlierTerminalAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := openJournal(t, path)
	f := newFlow(t, j, nil, nil, time.Unix(10, 0))
	for _, id := range []string{"a", "b"} {
		if _, err := f.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := j.MarkInputAccepted(ctx, "s", "a", "worker"); err != nil {
		t.Fatal(err)
	}
	j = openJournal(t, path)
	f = newFlow(t, j, nil, nil, time.Unix(20, 0))
	if ready, err := rootInputReady(f, ctx, "s", "b", 2); ready || err != nil {
		t.Fatalf("accepted prior admitted root: %v %v", ready, err)
	}
	for _, seq := range []uint64{0, 1, 3} {
		if _, err := rootInputReady(f, ctx, "s", "b", seq); !errors.Is(err, durableflow.ErrInvalidHandoff) {
			t.Fatal("stale root tuple passed")
		}
	}
	if _, err := rootInputReady(f, ctx, "s", "absent", 2); !errors.Is(err, durableflow.ErrInvalidHandoff) {
		t.Fatal("missing root accepted")
	}
	if _, err := j.ResolveAcceptedInput(ctx, "s", "a", 1, messagejournal.InputCompleted); err != nil {
		t.Fatal(err)
	}
	if ready, err := rootInputReady(f, ctx, "s", "b", 2); !ready || err != nil {
		t.Fatalf("terminal did not release root: %v %v", ready, err)
	}
}

func TestAsyncAcceptedNoticePreservesExactTerminalAndRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := openJournal(t, path)
	f := newFlow(t, j, nil, nil, time.Unix(10, 0))
	if _, err := f.EnqueueInput(ctx, "s", "m", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	var callback func(context.Context, durableflow.InputProcessResult) error
	var receipt durableflow.InputProcessResult
	p := inputProcessorFunc(func(ctx context.Context, in durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		callback = cb.OnCompleted
		receipt = durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessAccepted}
		if err := cb.OnCompleted(ctx, receipt); !errors.Is(err, durableflow.ErrInvalidHandoff) {
			t.Fatal("preacceptance notice accepted")
		}
		if err := cb.OnAccepted(ctx, handoffReceipt(in, durableflow.HandoffAccepted)); err != nil {
			return receipt, err
		}
		return receipt, nil
	})
	if _, err := f.ProcessNextInput(ctx, "s", p); err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []bool{false, true} {
		if terminal {
			if _, err := j.ResolveAcceptedInput(ctx, "s", "m", receipt.Sequence, messagejournal.InputCompleted); err != nil {
				t.Fatal(err)
			}
		}
		if err := callback(ctx, receipt); err != nil {
			t.Fatalf("exact accepted notice rejected: %v", err)
		}
		bad := receipt
		bad.Sequence++
		if err := callback(ctx, bad); !errors.Is(err, durableflow.ErrInvalidHandoff) {
			t.Fatal("invalid callback accepted")
		}
		want := messagejournal.InputAccepted
		if terminal {
			want = messagejournal.InputCompleted
		}
		inputs, err := openJournal(t, path).Inputs(ctx, "s")
		if err != nil || len(inputs) != 1 || inputs[0].Phase != want {
			t.Fatal("async notice changed durable outcome")
		}
	}
}

func TestRecoveryUncertainAcceptedAndPreacceptanceRemainDistinct(t *testing.T) {
	for _, mode := range []string{"unknown", "pending", "final_save_failure", "malformed"} {
		for _, accepted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/accepted_%v", mode, accepted), func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "journal.json")
				j := openJournal(t, path)
				f := newFlow(t, j, nil, nil, time.Unix(10, 0))
				for _, id := range []string{"a", "b"} {
					if _, err := f.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := j.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
					t.Fatal(err)
				}
				var err error
				if accepted {
					_, err = j.MarkInputAccepted(ctx, "s", "a", "worker")
				} else {
					_, err = j.MarkInputDeliveryUnknown(ctx, "s", "a", "worker")
				}
				if err != nil {
					t.Fatal(err)
				}
				fault := errors.New("synthetic final persistence failure")
				resolver := acceptedResolverFunc(func(_ context.Context, in durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
					out := durableflow.AcceptedResolutionResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, Resolution: durableflow.AcceptedUnknown}
					switch mode {
					case "pending":
						out.Resolution = durableflow.AcceptedPending
					case "malformed":
						out.Sequence++
					case "final_save_failure":
						return out, fault
					}
					return out, nil
				})
				results, err := f.ReconcileAcceptedInputs(ctx, "s", resolver)
				wantError := mode == "malformed" || mode == "final_save_failure" || mode == "pending" && !accepted
				if (err != nil) != wantError || mode == "final_save_failure" && !errors.Is(err, fault) {
					t.Fatalf("resolution error lost: %v", err)
				}
				wantPhase, wantResolution := messagejournal.InputUnknown, durableflow.AcceptedUnknown
				if accepted {
					wantPhase, wantResolution = messagejournal.InputAccepted, durableflow.AcceptedPending
				}
				if len(results) != 1 || results[0].Resolution != wantResolution {
					t.Fatalf("wrong recovery result: %#v", results)
				}
				inputs, err := openJournal(t, path).Inputs(ctx, "s")
				if err != nil || len(inputs) != 2 || inputs[0].Phase != wantPhase || inputs[1].Phase != messagejournal.InputPending {
					t.Fatalf("recovery changed custody: %#v %v", inputs, err)
				}
				if ready, err := f.RootInputReady(ctx, "s", "b", 2); ready || err != nil {
					t.Fatalf("pending recovery released root: %v %v", ready, err)
				}
				resolver = acceptedResolverFunc(func(_ context.Context, in durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
					return durableflow.AcceptedResolutionResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, Resolution: durableflow.AcceptedCompleted}, nil
				})
				for i := 0; i < 2; i++ {
					if _, err := f.ReconcileAcceptedInputs(ctx, "s", resolver); err != nil {
						t.Fatal(err)
					}
				}
				if ready, err := f.RootInputReady(ctx, "s", "b", 2); !ready || err != nil {
					t.Fatalf("exact terminal did not release root: %v %v", ready, err)
				}
			})
		}
	}
}

type terminalCommitFaultJournal struct {
	*messagejournal.Journal
	err error
}

func (j *terminalCommitFaultJournal) ResolveAcceptedInput(context.Context, string, string, uint64, messagejournal.InputPhase) (messagejournal.Input, error) {
	return messagejournal.Input{}, j.err
}

func TestFailedTerminalCommitReturnsAcceptedNotFalseCompletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	fault := errors.New("synthetic terminal commit failure")
	j := &terminalCommitFaultJournal{Journal: openJournal(t, path), err: fault}
	f := newFlow(t, j, nil, nil, time.Unix(10, 0))
	if _, err := f.EnqueueInput(ctx, "s", "a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	p := inputProcessorFunc(func(ctx context.Context, in durableflow.ProviderInput, cb durableflow.InputProcessCallbacks) (durableflow.InputProcessResult, error) {
		if err := cb.OnAccepted(ctx, handoffReceipt(in, durableflow.HandoffAccepted)); err != nil {
			t.Fatal(err)
		}
		return durableflow.InputProcessResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, State: durableflow.InputProcessCompleted}, nil
	})
	result, err := f.ProcessNextInput(ctx, "s", p)
	if !errors.Is(err, fault) || result.State != durableflow.InputProcessAccepted {
		t.Fatalf("uncommitted terminal advertised: %#v %v", result, err)
	}
	inputs, err := openJournal(t, path).Inputs(ctx, "s")
	if err != nil || len(inputs) != 1 || inputs[0].Phase != messagejournal.InputAccepted {
		t.Fatalf("acceptance lost: %#v %v", inputs, err)
	}
}

func TestMixedMalformedRecoveryRetainsUnresolvedAcceptances(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "journal.json")
	j := openJournal(t, path)
	f := newFlow(t, j, nil, nil, time.Unix(10, 0))
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := f.EnqueueInput(ctx, "s", id, []byte(id)); err != nil {
			t.Fatal(err)
		}
		if id == "d" {
			break
		}
		if _, err := j.LeaseNextInput(ctx, "s", "worker", time.Unix(10, 0), time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := j.MarkInputAccepted(ctx, "s", id, "worker"); err != nil {
			t.Fatal(err)
		}
	}
	resolver := acceptedResolverFunc(func(_ context.Context, in durableflow.AcceptedInput) (durableflow.AcceptedResolutionResult, error) {
		out := durableflow.AcceptedResolutionResult{SessionID: in.SessionID, MessageID: in.MessageID, Sequence: in.Sequence, Resolution: durableflow.AcceptedCompleted}
		if in.MessageID == "b" {
			out.Sequence++
		}
		return out, nil
	})
	results, err := f.ReconcileAcceptedInputs(ctx, "s", resolver)
	if !errors.Is(err, durableflow.ErrInvalidResolution) || len(results) != 2 || results[0].Resolution != durableflow.AcceptedCompleted || results[1].Resolution != durableflow.AcceptedPending {
		t.Fatalf("mixed reconciliation: %#v %v", results, err)
	}
	inputs, err := openJournal(t, path).Inputs(ctx, "s")
	if err != nil || len(inputs) != 4 || inputs[0].Phase != messagejournal.InputCompleted || inputs[1].Phase != messagejournal.InputAccepted || inputs[2].Phase != messagejournal.InputAccepted || inputs[3].Phase != messagejournal.InputPending {
		t.Fatalf("mixed recovery lost custody: %#v %v", inputs, err)
	}
	if ready, err := f.RootInputReady(ctx, "s", "d", 4); ready || err != nil {
		t.Fatalf("malformed proof released successor: %v %v", ready, err)
	}
}
