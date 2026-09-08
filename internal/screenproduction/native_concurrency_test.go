package screenproduction_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/screenproduction"
	"bria/internal/sessionruntime"
	"bria/internal/settings"
)

func TestNativeCacheConcurrentReceiptsAndCopiesRemainIsolated(t *testing.T) {
	preferences := settings.NewMemoryStore()
	if err := preferences.Update(context.Background(), func(s *settings.Settings) error {
		s.ScreenEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source, err := screenproduction.NewNativeSource(preferences, &selectedScreen{id: "active"}, &nativeScreens{
		available: true, snapshot: sessionruntime.NativeSnapshot{FullText: "concurrent screen", Hash: "snapshot"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ScreenPNG(context.Background(), "active"); err != nil {
		t.Fatal(err)
	}
	want := source.CachedScreen("active")
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				image := source.CachedScreen("active")
				if image.Hash != want.Hash || len(image.PNG) == 0 || image.PNG[0] != want.PNG[0] {
					t.Error("concurrent reader changed cached PNG")
					return
				}
				image.PNG[0] = 0
				if !source.RememberTelegramFileID("active", want.Hash, "confirmed-file") {
					t.Error("exact receipt rejected")
				}
			}
		}()
	}
	workers.Wait()
	if got := source.CachedScreen("active"); got.FileID != "confirmed-file" || got.PNG[0] != want.PNG[0] {
		t.Fatal("receipt or immutable PNG was lost")
	}
}

type firstRefreshSettings struct {
	settings.Store
	calls atomic.Int32
	next  chan struct{}
}

func (s *firstRefreshSettings) Load(context.Context) (settings.Settings, error) {
	call := s.calls.Add(1)
	if call == 2 {
		close(s.next)
	}
	return settings.Settings{ScreenEnabled: call == 1}, nil
}

type delayedActiveCheck struct {
	calls    atomic.Int32
	rendered chan struct{}
	release  chan struct{}
}

func (s *delayedActiveCheck) LoadActiveSession(context.Context) (domain.SessionID, error) {
	if s.calls.Add(1) == 2 {
		close(s.rendered)
		<-s.release
	}
	return "active", nil
}

func TestForgetDuringAsyncRefreshRejectsRenderedOldEpoch(t *testing.T) {
	preferences := &firstRefreshSettings{next: make(chan struct{})}
	selected := &delayedActiveCheck{rendered: make(chan struct{}), release: make(chan struct{})}
	source, err := screenproduction.NewNativeSource(preferences, selected, &nativeScreens{
		available: true, snapshot: sessionruntime.NativeSnapshot{FullText: "forgotten screen", Hash: "old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	defer release.Do(func() { close(selected.release) })
	if err := source.RequestScreenPNG(context.Background(), "active"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-selected.rendered:
	case <-time.After(5 * time.Second):
		t.Fatal("asynchronous screenshot did not reach active-session recheck")
	}
	source.ForgetSession("active")
	release.Do(func() { close(selected.release) })
	// A second refresh can enter settings only after the first request has
	// finished. It is disabled so it cannot replace or hide a stale image.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-preferences.next:
			if got := source.CachedScreen("active"); len(got.PNG) != 0 || got.Hash != "" || got.FileID != "" {
				t.Fatal("forgotten in-flight image restored lifecycle cache")
			}
			return
		case <-tick.C:
			if err := source.RequestScreenPNG(context.Background(), "active"); err != nil {
				t.Fatal(err)
			}
		case <-deadline.C:
			t.Fatal("asynchronous refresh did not finish")
		}
	}
}
