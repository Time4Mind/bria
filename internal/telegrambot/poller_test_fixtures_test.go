package telegrambot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type failOnceCursor struct {
	memoryCursor
	failed atomic.Bool
}

func (c *failOnceCursor) Commit(ctx context.Context, next int64) error {
	if !c.failed.Swap(true) {
		return errors.New("temporary cursor commit failure")
	}
	return c.memoryCursor.Commit(ctx, next)
}

type testLeadership struct{ leader atomic.Bool }

func (l *testLeadership) IsLeader() bool { return l.leader.Load() }

type memoryCursor struct {
	mu      sync.Mutex
	next    int64
	loads   int
	commits []int64
}

func (c *memoryCursor) Load(context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	return c.next, nil
}

func (c *memoryCursor) Commit(_ context.Context, next int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next = next
	c.commits = append(c.commits, next)
	return nil
}

type blockingAPI struct {
	calls    atomic.Int64
	started  chan struct{}
	canceled chan struct{}
	requests chan GetUpdatesRequest
	once     sync.Once
}

func (a *blockingAPI) GetUpdates(ctx context.Context, request GetUpdatesRequest) ([]Update, error) {
	a.calls.Add(1)
	a.once.Do(func() { close(a.started) })
	if a.requests != nil {
		a.requests <- request
	}
	<-ctx.Done()
	select {
	case <-a.canceled:
	default:
		close(a.canceled)
	}
	return nil, ctx.Err()
}

func (*blockingAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*blockingAPI) SendMessage(context.Context, MessageRequest) (Message, error) {
	return Message{}, nil
}
func (*blockingAPI) EditMessage(context.Context, EditMessageRequest) (Message, error) {
	return Message{}, nil
}

type batchAPI struct {
	requests chan GetUpdatesRequest
	served   atomic.Bool
}

func (a *batchAPI) GetUpdates(ctx context.Context, request GetUpdatesRequest) ([]Update, error) {
	a.requests <- request
	if !a.served.Swap(true) {
		return []Update{
			{UpdateID: 4, Message: &UpdateMessage{
				MessageID: 1, FromID: 42, ChatID: -7, ChatType: "group",
			}},
			{UpdateID: 5, Message: &UpdateMessage{
				MessageID: 2, FromID: 42, ChatID: 42, ChatType: "private", Text: "ok",
			}},
		}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*batchAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*batchAPI) SendMessage(context.Context, MessageRequest) (Message, error) {
	return Message{}, nil
}
func (*batchAPI) EditMessage(context.Context, EditMessageRequest) (Message, error) {
	return Message{}, nil
}

type singleUpdateAPI struct{ update Update }

func (a *singleUpdateAPI) GetUpdates(context.Context, GetUpdatesRequest) ([]Update, error) {
	return []Update{a.update}, nil
}
func (*singleUpdateAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*singleUpdateAPI) SendMessage(context.Context, MessageRequest) (Message, error) {
	return Message{}, nil
}
func (*singleUpdateAPI) EditMessage(context.Context, EditMessageRequest) (Message, error) {
	return Message{}, nil
}

type poisonCallbackAPI struct{}

func (*poisonCallbackAPI) GetUpdates(ctx context.Context, request GetUpdatesRequest) ([]Update, error) {
	if request.Offset <= 20 {
		return []Update{{UpdateID: 20, CallbackQuery: &CallbackQuery{
			ID: "poison", FromID: 42, Data: "menu",
			Message: &UpdateMessage{MessageID: 1, FromID: 42, ChatID: 42, ChatType: "private"},
		}}}, nil
	}
	if request.Offset == 21 {
		return []Update{{UpdateID: 21, Message: &UpdateMessage{
			MessageID: 2, FromID: 42, ChatID: 42, ChatType: "private", Text: "after",
		}}}, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*poisonCallbackAPI) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (*poisonCallbackAPI) SendMessage(context.Context, MessageRequest) (Message, error) {
	return Message{}, nil
}
func (*poisonCallbackAPI) EditMessage(context.Context, EditMessageRequest) (Message, error) {
	return Message{}, nil
}

func newTestPoller(
	t *testing.T,
	api API,
	leader Leadership,
	handler UpdateHandler,
) *Poller {
	t.Helper()
	return newTestPollerWithCursor(t, api, leader, &memoryCursor{}, handler)
}

func newTestPollerWithCursor(
	t *testing.T,
	api API,
	leader Leadership,
	cursor Cursor,
	handler UpdateHandler,
) *Poller {
	t.Helper()
	poller, err := NewPoller(PollerConfig{
		API: api, Leadership: leader, Cursor: cursor, Handler: handler,
		LongPollTimeout: time.Second, LeadershipCheckInterval: 5 * time.Millisecond,
		RetryDelay: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	return poller
}

func waitChannel(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
