package app_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
)

// Concurrent interactive requests must only schedule a working session. Only
// the turn-finishing caller may later close its provider. Yielding in the store
// increases mutex contention without changing any lifecycle result.
func TestConcurrentInteractiveBusyCloseDoesNotInterruptRunningWork(t *testing.T) {
	for attempt := 0; attempt < 200; attempt++ {
		running, err := readySession(t, time.Now().Add(-time.Hour)).StartWork(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		store := &concurrentCloseStore{session: running}
		provider := &concurrentCloseProvider{}
		closer, err := app.NewSessionCloser(store, provider, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		const callers = 32
		start := make(chan struct{})
		var ready, done sync.WaitGroup
		ready.Add(callers)
		done.Add(callers)
		var invalid atomic.Int32
		for i := 0; i < callers; i++ {
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				result, err := closer.BeginClose(context.Background(), running.ID())
				if err != nil || !result.Scheduled || result.Session.Status() != domain.SessionClosingAfterWork {
					invalid.Add(1)
				}
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		current, err := store.Load(context.Background(), running.ID())
		if provider.aborts.Load() != 0 || invalid.Load() != 0 || err != nil || current.Status() != domain.SessionClosingAfterWork {
			t.Fatalf("attempt %d: concurrent BeginClose interrupted unfinished work: aborts=%d invalid_results=%d durable_status=%s err=%v", attempt, provider.aborts.Load(), invalid.Load(), current.Status(), err)
		}
	}
}

type concurrentCloseStore struct {
	mu      sync.Mutex
	session domain.Session
}

func (s *concurrentCloseStore) Load(context.Context, domain.SessionID) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime.Gosched()
	return s.session, nil
}

func (s *concurrentCloseStore) Replace(_ context.Context, expected, next domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.session.Equal(expected) {
		return errors.New("synthetic lifecycle conflict")
	}
	s.session = next
	return nil
}

type concurrentCloseProvider struct {
	lifecycleStarter
	aborts atomic.Int32
}

func (s *concurrentCloseProvider) Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error {
	s.aborts.Add(1)
	return errors.New("synthetic provider still executing its request")
}
