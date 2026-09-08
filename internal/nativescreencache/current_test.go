package nativescreencache_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nativescreencache"
)

func TestCurrentDeliveryDiscardsInvalidatedFrameAndNeverWaitsForBusyRender(t *testing.T) {
	for _, change := range []string{"forget", "disabled", "switch", "cancel"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var enabled atomic.Bool
			enabled.Store(true)
			var switched atomic.Bool
			var calls atomic.Int32
			source, err := nativescreencache.New(nativescreencache.Config{
				Preferences: func(context.Context) (nativescreencache.Preferences, error) {
					return nativescreencache.Preferences{ScreenEnabled: enabled.Load()}, nil
				},
				ActiveSession: func(context.Context) (domain.SessionID, error) {
					if calls.Add(1) == 2 {
						close(entered)
						<-release
					}
					if switched.Load() {
						return "other", nil
					}
					return "active", nil
				},
				Snapshot: func(domain.SessionID) (nativescreencache.Snapshot, bool) {
					return nativescreencache.Snapshot{FullText: "exact current snapshot", Hash: "current"}, true
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				png          []byte
				hash, fileID string
				err          error
			}
			done := make(chan result, 1)
			go func() {
				png, hash, fileID, err := source.CurrentScreenDelivery(ctx, "active")
				done <- result{png, hash, fileID, err}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("render did not reach active recheck")
			}
			busy := make(chan result, 1)
			go func() {
				png, hash, fileID, err := source.CurrentScreenDelivery(ctx, "active")
				busy <- result{png, hash, fileID, err}
			}()
			select {
			case got := <-busy:
				if len(got.png) != 0 || got.hash != "" || got.fileID != "" || got.err != nil {
					close(release)
					t.Fatal("busy render exposed image")
				}
			case <-time.After(time.Second):
				close(release)
				t.Fatal("busy render blocked another card")
			}
			switch change {
			case "forget":
				source.ForgetSession("active")
			case "disabled":
				enabled.Store(false)
			case "switch":
				switched.Store(true)
			case "cancel":
				cancel()
			}
			close(release)
			select {
			case got := <-done:
				if len(got.png) != 0 || got.hash != "" || got.fileID != "" {
					t.Fatal("invalidated frame exposed")
				}
				if change == "cancel" && !errors.Is(got.err, context.Canceled) {
					t.Fatal("lost cancellation")
				}
				if len(source.CachedScreenPNG("active")) != 0 {
					t.Fatal("invalidated frame published into cache")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("current render did not finish")
			}
		})
	}
}
