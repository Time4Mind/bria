package nativescreencache_test

import (
	"bytes"
	"context"
	"image/png"
	"sync"
	"testing"
	"time"

	"bria/internal/domain"
	"bria/internal/nativescreencache"
)

func TestRefreshReturnsBeforeCaptureAndOutlivesCardContext(t *testing.T) {
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "card"))
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var enteredOnce sync.Once
	defer once.Do(func() { close(release) })
	source, err := nativescreencache.New(nativescreencache.Config{
		Preferences: func(ctx context.Context) (nativescreencache.Preferences, error) {
			enteredOnce.Do(func() { close(entered) })
			<-release
			if ctx.Err() != nil || ctx.Value(contextKey{}) != "card" {
				t.Error("refresh lost context values or inherited card cancellation")
			}
			return nativescreencache.Preferences{ScreenEnabled: true}, nil
		},
		ActiveSession: func(context.Context) (domain.SessionID, error) { return "active", nil },
		Snapshot: func(domain.SessionID) (nativescreencache.Snapshot, bool) {
			return nativescreencache.Snapshot{FullText: "ready screenshot", Hash: "native-hash"}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan error, 1)
	go func() { returned <- source.RequestScreenPNG(ctx, "active") }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("text card waited for screenshot preferences")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not start")
	}
	if len(source.CachedScreenPNG("active")) != 0 {
		t.Fatal("unfinished capture was exposed")
	}
	// A second request while preferences are blocked must return immediately.
	go func() { returned <- source.RequestScreenPNG(ctx, "active") }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending refresh blocked a second card")
	}
	cancel()
	once.Do(func() { close(release) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			data, hash, fileID := source.CachedScreenDelivery("active")
			if len(data) == 0 {
				continue
			}
			if _, err := png.Decode(bytes.NewReader(data)); err != nil || len(hash) != 64 || fileID != "" {
				t.Fatalf("ready screenshot identity: hash=%q file=%q err=%v", hash, fileID, err)
			}
			if !source.RememberTelegramFileID("active", hash, "confirmed") {
				t.Fatal("ready PNG could not accept its receipt")
			}
			got, gotHash, gotFile := source.CachedScreenDelivery("active")
			if !bytes.Equal(got, data) || gotHash != hash || gotFile != "confirmed" {
				t.Fatal("delivery did not reuse exact confirmed PNG")
			}
			return
		case <-deadline.C:
			t.Fatal("refresh did not publish a ready screenshot after card cancellation")
		}
	}
}
