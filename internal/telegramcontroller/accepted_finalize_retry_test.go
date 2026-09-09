package telegramcontroller_test

import (
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
	"bria/internal/turncontinuation"
	"bria/internal/turnprocessing"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type retryObservationAttachments struct {
	mu        sync.Mutex
	fail      string
	failed    bool
	completed map[string]bool
	fault     chan struct{}
}

func (s *retryObservationAttachments) MarkAccepted(context.Context, turnprocessing.AttachmentReceipt) error {
	return errors.New("must not reaccept")
}
func (s *retryObservationAttachments) MarkCompleted(_ context.Context, r turnprocessing.AttachmentReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.MessageID == s.fail && !s.failed {
		s.failed = true
		close(s.fault)
		return errors.New("synthetic attachment commit failure")
	}
	s.completed[r.MessageID] = true
	return nil
}

func TestAcceptedKnownTerminalRetriesLocalFinalizationWithoutObservation(t *testing.T) {
	for _, fault := range []string{"root-journal", "steer-journal", "root-attachment", "steer-attachment", "output"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
			running, err := ready.StartWork(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := running.Binding()
			sessions := newLockedSessions(running)
			failed := make(chan struct{})
			settled := make(chan struct{})
			custody := &retryObservationAttachments{completed: map[string]bool{}, fault: failed}
			if fault == "root-attachment" {
				custody.fail = "root"
			}
			if fault == "steer-attachment" {
				custody.fail = "steer"
			}
			var observations, finishes atomic.Int32
			provider := &acceptedObserver{observe: func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				observations.Add(1)
				return sessionruntime.TurnResult{TerminalStatus: sessionruntime.StatusCompleted, Final: "retained exact final"}, nil
			}}
			var mu sync.Mutex
			outputs := map[string]string{}
			completed := map[string]bool{}
			faulted := false
			output := durableOutputFunc(func(_ context.Context, n telegramcontroller.OutgoingNotification) (telegramcontroller.OutputReceipt, error) {
				mu.Lock()
				defer mu.Unlock()
				if n.Kind == telegramcontroller.NotificationFinal && fault == "output" && !faulted {
					faulted = true
					close(failed)
					return telegramcontroller.OutputReceipt{}, errors.New("synthetic output journal failure")
				}
				if n.Kind == telegramcontroller.NotificationFinal {
					outputs[n.OperationID] = string(n.Payload)
				}
				return telegramcontroller.OutputReceipt{SessionID: n.SessionID, OperationID: n.OperationID, Sequence: 1}, nil
			})
			c := newController(t, nil, sessions, provider, nil, telegramcontroller.Options{AcceptedObserver: provider, Attachments: custody, DurableOutput: output, UIState: &retryFinalState{attempt: make(chan struct{}, 16)}, TurnLifecycle: turnLifecycleFunc{finish: func(context.Context, domain.SessionID) (domain.Session, bool, error) {
				finishes.Add(1)
				sessions.Set(ready)
				return ready, false, nil
			}}})
			defer c.Close(context.Background())
			var members []turncontinuation.Member
			for i, id := range []string{"root", "steer"} {
				members = append(members, turncontinuation.Member{Input: telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: id, Sequence: uint64(i + 1), Attachments: []turnprocessing.AttachmentRef{{Reference: id, Size: 10, SHA256: "abc"}}}, TurnID: "native-turn", Accepted: true, Callbacks: telegramcontroller.DurableInputCallbacks{OnCompleted: func(_ context.Context, r telegramcontroller.DurableInputProcessReceipt) error {
					mu.Lock()
					defer mu.Unlock()
					if r.Completion != telegramcontroller.DurableInputSucceeded {
						return errors.New("known terminal lost")
					}
					if fault == r.MessageID+"-journal" && !faulted {
						faulted = true
						close(failed)
						return errors.New("synthetic journal failure")
					}
					completed[r.MessageID] = true
					return nil
				}}})
			}
			if err := c.ContinueAcceptedBatch(ctx, binding, members, func() { close(settled) }); err != nil {
				t.Fatal(err)
			}
			select {
			case <-failed:
			case <-ctx.Done():
				t.Fatal("fault boundary not reached")
			}
			if finishes.Load() != 0 {
				t.Fatal("failed finalization admitted Ready")
			}
			select {
			case <-settled:
			case <-ctx.Done():
				t.Fatal("known terminal not retried without observing again")
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			custody.mu.Lock()
			defer custody.mu.Unlock()
			if observations.Load() != 1 || provider.submits.Load() != 0 || finishes.Load() != 1 || len(outputs) != 1 || outputs["root:final"] != "retained exact final" || len(completed) != 2 || len(custody.completed) != 2 {
				t.Fatal("retry duplicated native work or lost exact final/custody")
			}
		})
	}
}
