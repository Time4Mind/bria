package telegramcontroller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

func TestExactACKSurvivesLocalAcceptanceFailure(t *testing.T) {
	for _, steer := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "steer"}[steer], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
			release := make(chan struct{})
			prompts := &ackPromptStore{}
			c := newController(t, nil, newLockedSessions(ready), terminalFailureProvider{release: release}, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: prompts})
			defer func() { close(release); _ = c.Close(context.Background()) }()
			if steer {
				_, err := c.ProcessDurableInput(ctx, telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "root", Sequence: 1, Payload: []byte("root")}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return nil }})
				if err != nil {
					t.Fatal(err)
				}
			}
			fault := errors.New("synthetic acceptance persistence failure")
			input := telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "accepted", Sequence: 2, Payload: []byte("synthetic")}
			receipt, err := c.ProcessDurableInput(ctx, input, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error { return fault }})
			if !receipt.Accepted || receipt.Completion != telegramcontroller.DurableInputAwaitingRecovery || !errors.Is(err, fault) || receipt.SessionID != input.SessionID || receipt.MessageID != input.MessageID || receipt.Sequence != input.Sequence {
				t.Fatalf("exact ACK lost or failed commit hidden: %+v %v", receipt, err)
			}
			if !prompts.has(ready.ID(), input.MessageID, "👨‍💻 synthetic") {
				t.Fatal("exact ACK did not persist its accepted prompt anchor")
			}
		})
	}
}

type ackPromptStore struct {
	mu      sync.Mutex
	entries [][3]string
}

func (*ackPromptStore) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (s *ackPromptStore) SetCardPrompt(_ context.Context, id domain.SessionID, message, entry string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, [3]string{string(id), message, entry})
	return nil
}
func (s *ackPromptStore) has(id domain.SessionID, message, entry string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, got := range s.entries {
		if got == [3]string{string(id), message, entry} {
			return true
		}
	}
	return false
}

func TestMismatchedACKDoesNotPublishAcceptedPrompt(t *testing.T) {
	ready := readySession(t, "aaaaaaaa-aaaa-4aaa-9aaa-aaaaaaaaaaaa", domain.ProviderCodex, t.TempDir(), "provider-a", 1)
	prompts := &ackPromptStore{}
	provider := &interactiveSubmitter{submitWithCallbacks: func(_ context.Context, _ domain.SessionID, _ string, cb sessionruntime.TurnCallbacks) (sessionruntime.TurnResult, error) {
		return sessionruntime.TurnResult{}, cb.OnAccepted("other-message")
	}}
	c := newController(t, nil, newLockedSessions(ready), provider, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, UIState: prompts})
	defer c.Close(context.Background())
	r, err := c.ProcessDurableInput(context.Background(), telegramcontroller.DurableLeasedInput{SessionID: ready.ID(), MessageID: "expected", Sequence: 1, Payload: []byte("synthetic")}, telegramcontroller.DurableInputCallbacks{OnAccepted: func(context.Context, telegramcontroller.DurableInputAcceptance) error {
		t.Error("mismatched ACK reached custody")
		return nil
	}})
	if err == nil || r.Accepted || prompts.has(ready.ID(), "expected", "👨‍💻 synthetic") || prompts.has(ready.ID(), "other-message", "👨‍💻 synthetic") {
		t.Fatalf("mismatched ACK fabricated accepted prompt: %+v %v", r, err)
	}
}
