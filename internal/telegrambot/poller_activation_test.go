package telegrambot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

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
