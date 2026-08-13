package telegrambot

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type conflictAPI struct{ calls atomic.Int64 }

func (a *conflictAPI) GetUpdates(context.Context, GetUpdatesRequest) ([]Update, error) {
	a.calls.Add(1)
	return nil, &APIError{Method: "getUpdates", Code: http.StatusConflict,
		Description: "terminated by other getUpdates request"}
}
func (*conflictAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*conflictAPI) SendMessage(context.Context, MessageRequest) (Message, error) {
	return Message{}, nil
}
func (*conflictAPI) EditMessage(context.Context, EditMessageRequest) (Message, error) {
	return Message{}, nil
}

func TestPollingConflictRetriesButDoesNotArbitrateLeadership(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	api := &conflictAPI{}
	poller := newTestPoller(t, api, leader, UpdateHandlerFunc(func(
		context.Context, IncomingUpdate,
	) error {
		t.Fatal("conflicting poller handled an update")
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := poller.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("poller error=%v", err)
	}
	if api.calls.Load() < 2 || !leader.IsLeader() {
		t.Fatalf("calls=%d leader=%v", api.calls.Load(), leader.IsLeader())
	}
}
