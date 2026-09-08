package telegramcontroller_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turnprocessing"
)

func TestTerminalFailureRequiresAcceptedProofAndSuccessfulFinalization(t *testing.T) {
	for _, tc := range []struct {
		name, status, fault string
		want                telegramcontroller.DurableInputCompletion
	}{
		{"failed", sessionruntime.StatusFailed, "", telegramcontroller.DurableInputTerminalFailed},
		{"interrupted", sessionruntime.StatusInterrupted, "", telegramcontroller.DurableInputTerminalFailed},
		{"unknown", "", "", telegramcontroller.DurableInputCompletion("awaiting_recovery")},
		{"lifecycle-failure", sessionruntime.StatusFailed, "finish", telegramcontroller.DurableInputFailed},
		{"invalid-finished-state", sessionruntime.StatusFailed, "state", telegramcontroller.DurableInputFailed},
		{"close-failure", sessionruntime.StatusFailed, "close", telegramcontroller.DurableInputFailed},
		{"history-failure", sessionruntime.StatusFailed, "history", telegramcontroller.DurableInputCompletion("awaiting_recovery")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			release := make(chan struct{})
			close(release)
			options := telegramcontroller.Options{Recovered: []domain.Session{ready}}
			if tc.fault == "history" {
				options.UIState = terminalHistoryFailure{}
			}
			if tc.fault == "finish" || tc.fault == "state" || tc.fault == "close" {
				running, err := ready.StartWork(time.Now())
				if err != nil {
					t.Fatal(err)
				}
				options.TurnLifecycle = turnLifecycleFunc{
					start: func(context.Context, domain.SessionID) (domain.Session, error) { return running, nil },
					finish: func(context.Context, domain.SessionID) (domain.Session, bool, error) {
						if tc.fault == "finish" {
							return domain.Session{}, false, errors.New("synthetic lifecycle failure")
						}
						if tc.fault == "close" {
							closing, err := running.CloseAfterWork(time.Now())
							return closing, true, err
						}
						return running, false, nil
					},
				}
				options.SessionCloser = sessionCloserFunc(func(context.Context, domain.SessionID) (app.CloseSessionResult, error) {
					return app.CloseSessionResult{}, errors.New("synthetic close failure")
				})
			}
			controller := newController(t, nil, newLockedSessions(ready), terminalFailureProvider{release: release, status: tc.status}, nil, options)
			t.Cleanup(func() { _ = controller.Close(context.Background()) })
			completed := make(chan telegramcontroller.DurableInputProcessReceipt, 2)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			receipt, err := controller.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "root", Sequence: 1, Payload: []byte("synthetic")}, telegramcontroller.DurableInputCallbacks{
				OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
				OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
					completed <- receipt
					return nil
				},
			})
			if err != nil || !receipt.Accepted {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			select {
			case receipt := <-completed:
				if receipt.Completion != tc.want || !receipt.Accepted || receipt.SessionID != ready.ID() || receipt.MessageID != "root" || receipt.Sequence != 1 {
					t.Fatalf("completion=%+v want=%s", receipt, tc.want)
				}
			case <-ctx.Done():
				t.Fatal("missing finalization result")
			}
			if tc.want != telegramcontroller.DurableInputTerminalFailed {
				next, err := controller.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "next", Sequence: 2, Payload: []byte("next")}, telegramcontroller.DurableInputCallbacks{
					OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
						t.Error("blocked predecessor admitted next input")
						return nil
					},
				})
				if next.Accepted || err == nil || tc.fault != "close" && !errors.Is(err, turnprocessing.ErrInputDeferred) {
					t.Fatalf("blocked outcome did not defer next input: %+v %v", next, err)
				}
			}
		})
	}
}

func TestUnacceptedProviderFailureDoesNotPublishTerminalFailure(t *testing.T) {
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	provider := &interactiveSubmitter{submitWithCallbacks: func(context.Context, domain.SessionID, string, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusFailed}, sessionruntime.ErrTurnFailed
	}}
	controller := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}})
	defer controller.Close(context.Background())
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 1)
	receipt, err := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "root", Sequence: 1, Payload: []byte("synthetic")}, telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
			t.Error("unexpected acceptance")
			return nil
		},
		OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
			completed <- receipt
			return nil
		},
	})
	if err == nil || receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputFailed {
		t.Fatalf("unaccepted failure promoted: %+v, %v", receipt, err)
	}
	select {
	case result := <-completed:
		t.Fatalf("unaccepted completion published: %+v", result)
	default:
	}
	next, err := controller.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "next", Sequence: 2, Payload: []byte("next")}, telegramcontroller.DurableInputCallbacks{
		OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
			t.Error("unaccepted failure unblocked next input")
			return nil
		},
	})
	if next.Accepted || !errors.Is(err, turnprocessing.ErrInputDeferred) {
		t.Fatalf("unaccepted failure did not block next input: %+v %v", next, err)
	}
}

type terminalHistoryFailure struct{}

func (terminalHistoryFailure) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (terminalHistoryFailure) LoadCardHistory(context.Context, domain.SessionID) ([]string, error) {
	return nil, nil
}
func (terminalHistoryFailure) AppendCardHistory(context.Context, domain.SessionID, string) error {
	return errors.New("synthetic history failure")
}

func TestTerminalFailureMainAndSteerShareCustodyFailure(t *testing.T) {
	t.Run("failure", func(t *testing.T) { testTerminalCustody(t, true) })
	t.Run("success-once", func(t *testing.T) { testTerminalCustody(t, false) })
}

func testTerminalCustody(t *testing.T, fail bool) {
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	release := make(chan struct{})
	provider := terminalFailureProvider{release: release, status: sessionruntime.StatusFailed}
	controller := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{
		Recovered: []domain.Session{ready}, Attachments: &terminalFailureCustody{fail: fail},
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })
	completed := make(chan telegramcontroller.DurableInputProcessReceipt, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for index, message := range []string{"root", "steer"} {
		input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: message, Sequence: uint64(index + 1), Payload: []byte(message)}
		if index == 0 {
			input.Attachments = []telegramcontroller.AttachmentRef{{Reference: "photo-a", Size: 1, SHA256: strings.Repeat("a", 64)}}
		}
		receipt, err := controller.ProcessDurableInput(ctx, input, telegramcontroller.DurableInputCallbacks{
			OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil },
			OnCompleted: func(_ context.Context, receipt telegramcontroller.DurableInputProcessReceipt) error {
				completed <- receipt
				return nil
			},
		})
		if err != nil || !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputPending {
			t.Fatalf("early %s receipt=%+v err=%v", message, receipt, err)
		}
	}
	close(release)
	seen := map[string]bool{}
	want := telegramcontroller.DurableInputTerminalFailed
	if fail {
		want = telegramcontroller.DurableInputCompletion("awaiting_recovery")
	}
	for len(seen) < 2 {
		select {
		case receipt := <-completed:
			if seen[receipt.MessageID] || receipt.Completion != want {
				t.Fatalf("custody must resolve both root and steer once: %+v want=%s", receipt, want)
			}
			seen[receipt.MessageID] = true
		case <-ctx.Done():
			t.Fatal("missing shared terminal result")
		}
	}
}

type terminalFailureProvider struct {
	release <-chan struct{}
	status  string
}

func (p terminalFailureProvider) Submit(ctx context.Context, id domain.SessionID, text string) (sessionruntime.TurnResult, error) {
	return p.SubmitWithCallbacks(ctx, id, text, sessionruntime.TurnCallbacks{})
}
func (p terminalFailureProvider) SubmitWithCallbacks(ctx context.Context, _ domain.SessionID, _ string, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	if err := callbacks.OnAccepted(callbacks.MessageID); err != nil {
		return sessionruntime.TurnResult{}, err
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return sessionruntime.TurnResult{}, ctx.Err()
	}
	return sessionruntime.TurnResult{TerminalStatus: p.status}, sessionruntime.ErrTurnFailed
}
func (p terminalFailureProvider) SubmitPreparedWithCallbacks(ctx context.Context, id domain.SessionID, input telegramcontroller.PreparedInput, callbacks sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
	return p.SubmitWithCallbacks(ctx, id, input.Text, callbacks)
}
func (p terminalFailureProvider) SubmitCurrentWithCallbacks(_ context.Context, _ domain.SessionID, _ sessionruntime.StructuredInput, callbacks sessionruntime.TurnCallbacks) error {
	return callbacks.OnAccepted(callbacks.MessageID)
}

type terminalFailureCustody struct {
	fail      bool
	completed atomic.Bool
}

func (*terminalFailureCustody) MarkAccepted(context.Context, telegramcontroller.AttachmentReceipt) error {
	return nil
}
func (c *terminalFailureCustody) MarkCompleted(_ context.Context, receipt telegramcontroller.AttachmentReceipt) error {
	if !c.completed.CompareAndSwap(false, true) || receipt != (telegramcontroller.AttachmentReceipt{Reference: "photo-a", ProviderSession: "provider-a", MessageID: "root"}) {
		return errors.New("duplicate or mismatched custody completion")
	}
	if c.fail {
		return errors.New("synthetic custody write failure")
	}
	return nil
}
