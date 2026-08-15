package telegrambot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestPollerCallsGetUpdatesOnlyAsLeader(t *testing.T) {
	leader := &testLeadership{}
	api := &blockingAPI{started: make(chan struct{}), canceled: make(chan struct{})}
	poller := newTestPoller(t, api, leader, UpdateHandlerFunc(func(
		context.Context, IncomingUpdate,
	) error {
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	time.Sleep(30 * time.Millisecond)
	if calls := api.calls.Load(); calls != 0 {
		t.Fatalf("follower made %d getUpdates calls", calls)
	}
	leader.leader.Store(true)
	waitChannel(t, api.started, "leader getUpdates call")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop after context cancellation")
	}
}

func TestPollerCancelsLongPollOnLeadershipLoss(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	api := &blockingAPI{
		started: make(chan struct{}), canceled: make(chan struct{}),
		requests: make(chan GetUpdatesRequest, 2),
	}
	cursor := &memoryCursor{}
	poller := newTestPollerWithCursor(t, api, leader, cursor, UpdateHandlerFunc(func(
		context.Context, IncomingUpdate,
	) error {
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	waitChannel(t, api.started, "initial long poll")
	<-api.requests
	leader.leader.Store(false)
	waitChannel(t, api.canceled, "long-poll cancellation")
	time.Sleep(30 * time.Millisecond)
	if calls := api.calls.Load(); calls != 1 {
		t.Fatalf("poller made %d calls after losing leadership", calls)
	}
	cursor.mu.Lock()
	cursor.next = 88
	cursor.mu.Unlock()
	leader.leader.Store(true)
	select {
	case request := <-api.requests:
		if request.Offset != 88 {
			t.Fatalf("reacquired-leader offset = %d, want 88", request.Offset)
		}
	case <-time.After(time.Second):
		t.Fatal("poller did not restart after leadership reacquisition")
	}
	cursor.mu.Lock()
	loads := cursor.loads
	cursor.mu.Unlock()
	if loads != 2 {
		t.Fatalf("cursor loads = %d, want one per leader epoch", loads)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
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

func TestPollerDropsNonDMUpdatesAndAdvancesOffset(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	api := &batchAPI{requests: make(chan GetUpdatesRequest, 2)}
	handled := make(chan IncomingUpdate, 1)
	poller := newTestPoller(t, api, leader, UpdateHandlerFunc(func(
		_ context.Context, update IncomingUpdate,
	) error {
		handled <- update
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	first := <-api.requests
	if first.Offset != 0 {
		t.Fatalf("initial offset = %d", first.Offset)
	}
	select {
	case update := <-handled:
		if update.Text != "ok" {
			t.Fatalf("unexpected handled update: %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("private update was not handled")
	}
	second := <-api.requests
	if second.Offset != 6 {
		t.Fatalf("next offset = %d, want 6", second.Offset)
	}
	cancel()
	<-done
	cursor := poller.cursor.(*memoryCursor)
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if len(cursor.commits) != 2 || cursor.commits[0] != 5 || cursor.commits[1] != 6 {
		t.Fatalf("cursor commits = %#v, want [5 6]", cursor.commits)
	}
}

func TestPollerStartsFromDurableCursor(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	api := &blockingAPI{
		started: make(chan struct{}), canceled: make(chan struct{}),
		requests: make(chan GetUpdatesRequest, 1),
	}
	cursor := &memoryCursor{next: 73}
	poller := newTestPollerWithCursor(t, api, leader, cursor, UpdateHandlerFunc(func(
		context.Context, IncomingUpdate,
	) error {
		return nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	waitChannel(t, api.started, "cursor-backed long poll")
	request := <-api.requests
	if request.Offset != 73 {
		t.Fatalf("poller offset = %d, want durable cursor 73", request.Offset)
	}
	cancel()
	<-done
}

func TestPollerActivatesLeaderBeforeLoadingCursor(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	api := &blockingAPI{
		started: make(chan struct{}), canceled: make(chan struct{}),
		requests: make(chan GetUpdatesRequest, 1),
	}
	cursor := &memoryCursor{next: 73}
	var activations atomic.Int64
	poller, err := NewPoller(PollerConfig{
		API: api, Leadership: leader, Cursor: cursor,
		Handler:         UpdateHandlerFunc(func(context.Context, IncomingUpdate) error { return nil }),
		LongPollTimeout: time.Second, LeadershipCheckInterval: 5 * time.Millisecond,
		RetryDelay: time.Millisecond,
		OnLeaderActivated: func(context.Context) error {
			if activations.Add(1) == 1 {
				return errors.New("temporary activation failure")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	waitChannel(t, api.started, "activated leader poll")
	if calls := activations.Load(); calls != 2 {
		t.Fatalf("leader activations=%d want=2", calls)
	}
	cursor.mu.Lock()
	loads := cursor.loads
	cursor.mu.Unlock()
	if loads != 1 {
		t.Fatalf("cursor loads=%d want=1", loads)
	}
	cancel()
	<-done
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

func TestHandlerErrorRetriesWithoutAdvancingDurableCursor(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	cursor := &memoryCursor{next: 9}
	wantErr := errors.New("handler failed")
	poller := newTestPollerWithCursor(t, &singleUpdateAPI{update: Update{
		UpdateID: 9,
		Message: &UpdateMessage{
			MessageID: 1, FromID: 42, ChatID: 42, ChatType: "private", Text: "retry me",
		},
	}}, leader, cursor, UpdateHandlerFunc(func(context.Context, IncomingUpdate) error {
		return wantErr
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := poller.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want context deadline", err)
	}
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if cursor.next != 9 || len(cursor.commits) != 0 {
		t.Fatalf("cursor advanced after handler failure: next=%d commits=%#v", cursor.next, cursor.commits)
	}
}

func TestPoisonCallbackIsDroppedAfterBoundedRetriesAndQueueContinues(t *testing.T) {
	leader := &testLeadership{}
	leader.leader.Store(true)
	cursor := &memoryCursor{next: 20}
	api := &poisonCallbackAPI{}
	callbackCalls := 0
	messageHandled := make(chan struct{}, 1)
	dropped := make(chan int, 1)
	poller, err := NewPoller(PollerConfig{
		API: api, Leadership: leader, Cursor: cursor,
		Handler: UpdateHandlerFunc(func(_ context.Context, update IncomingUpdate) error {
			if update.Kind == IncomingCallback {
				callbackCalls++
				return errors.New("permanent callback failure")
			}
			messageHandled <- struct{}{}
			return nil
		}),
		LongPollTimeout: time.Second, LeadershipCheckInterval: 5 * time.Millisecond,
		RetryDelay: time.Millisecond, MaxCallbackAttempts: 3,
		OnCallbackDropped: func(_ IncomingUpdate, _ error, attempts int) { dropped <- attempts },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	select {
	case <-messageHandled:
	case <-time.After(time.Second):
		t.Fatal("update after poison callback was blocked")
	}
	if callbackCalls != 3 {
		t.Fatalf("callback calls=%d want=3", callbackCalls)
	}
	select {
	case attempts := <-dropped:
		if attempts != 3 {
			t.Fatalf("drop attempts=%d", attempts)
		}
	default:
		t.Fatal("callback drop was not reported")
	}
	cancel()
	<-done
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if cursor.next != 22 {
		t.Fatalf("cursor=%d want=22", cursor.next)
	}
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
