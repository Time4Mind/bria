package telegrambot

import (
	"context"
	"errors"
	"testing"
	"time"
)

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

func TestRateLimitedCallbackIsDroppedWithoutImmediateRetries(t *testing.T) {
	poller := &Poller{
		maxCallbackAttempts: 5, callbackAttempts: make(map[int64]int),
	}
	dropped := 0
	poller.onCallbackDropped = func(_ IncomingUpdate, _ error, attempts int) {
		dropped = attempts
	}
	update := IncomingUpdate{UpdateID: 91, Kind: IncomingCallback}
	err := &APIError{Method: "editMessageText", Code: 429, RetryAfter: time.Minute}
	if !poller.dropFailedCallback(update, err) {
		t.Fatal("rate-limited callback was scheduled for immediate retry")
	}
	if dropped != 1 {
		t.Fatalf("drop attempts=%d want=1", dropped)
	}
	if len(poller.callbackAttempts) != 0 {
		t.Fatalf("rate-limited callback attempts=%#v", poller.callbackAttempts)
	}
}
