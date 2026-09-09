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
	"testing"
	"time"
)

type observationAttachmentCustody struct {
	mu        sync.Mutex
	completed map[string]int
}

func (s *observationAttachmentCustody) MarkAccepted(context.Context, turnprocessing.AttachmentReceipt) error {
	return errors.New("observation cannot accept attachment again")
}
func (s *observationAttachmentCustody) MarkCompleted(_ context.Context, r turnprocessing.AttachmentReceipt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed[r.MessageID]++
	return nil
}

func TestAcceptedContinuationRetainsRootAndSteerAttachmentsUntilNativeTerminal(t *testing.T) {
	for _, status := range []string{"", sessionruntime.StatusCompleted, sessionruntime.StatusFailed} {
		t.Run("terminal="+status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "native", 2)
			running, err := ready.StartWork(time.Now())
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := running.Binding()
			custody := &observationAttachmentCustody{completed: map[string]int{}}
			provider := &persistentAcceptedObserver{&acceptedObserver{observe: func(context.Context, domain.SessionID, domain.ProviderBinding, sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
				if status == sessionruntime.StatusCompleted {
					return sessionruntime.TurnResult{TerminalStatus: status, Final: "known final"}, nil
				}
				return sessionruntime.TurnResult{TerminalStatus: status}, errors.New("synthetic transport or terminal failure")
			}}}
			c := newController(t, nil, newLockedSessions(running), provider, nil, telegramcontroller.Options{UIState: &retryFinalState{attempt: make(chan struct{}, 8)}, Attachments: custody, AcceptedObserver: provider, Recoverer: deferredRecoverer(func(context.Context, domain.SessionID) (domain.Session, error) { return running, nil })})
			defer c.Close(context.Background())
			done := make(chan struct{}, 2)
			var members []turncontinuation.Member
			for i, id := range []string{"root", "steer"} {
				members = append(members, turncontinuation.Member{Input: telegramcontroller.DurableLeasedInput{SessionID: running.ID(), MessageID: id, Sequence: uint64(i + 1), Attachments: []turnprocessing.AttachmentRef{{Reference: "attachment-" + id, Size: 12, SHA256: "abc"}}}, TurnID: "shared-native-turn", Accepted: true, Callbacks: telegramcontroller.DurableInputCallbacks{OnCompleted: func(context.Context, telegramcontroller.DurableInputProcessReceipt) error {
					done <- struct{}{}
					return nil
				}}})
			}
			if err := c.ContinueAcceptedBatch(ctx, binding, members, nil); err != nil {
				t.Fatal(err)
			}
			for range members {
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal("group did not settle")
				}
			}
			if err := c.Close(ctx); err != nil {
				t.Fatal(err)
			}
			custody.mu.Lock()
			defer custody.mu.Unlock()
			if status == "" && len(custody.completed) != 0 {
				t.Fatal("observation loss released in-flight native attachments")
			}
			if status != "" && (len(custody.completed) != 2 || custody.completed["root"] != 1 || custody.completed["steer"] != 1) {
				t.Fatal("proven terminal did not complete each exact attachment once")
			}
		})
	}
}
