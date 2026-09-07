package sessionruntime_test

import (
	"bria/internal/sessionruntime"
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeControlDoesNotEnterTurnStream(t *testing.T) {
	starter, request, binding := startHelper(t, "hold", sessionruntime.Options{})
	defer func() { _ = starter.Abort(context.Background(), request, binding) }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	accepted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := starter.SubmitWithCallbacks(ctx, request.SessionID, "first", sessionruntime.TurnCallbacks{OnAccepted: func(string) error { close(accepted); return nil }})
		done <- err
	}()
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	snapshot, err := starter.NativeControl(ctx, request.SessionID, sessionruntime.NativeRequest{Command: "/model"})
	if err != nil || snapshot.Model != "actual-model" || !snapshot.Interactive {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if err := starter.StopCurrent(ctx, request.SessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	result, err := starter.Submit(ctx, request.SessionID, "after")
	if err != nil || result.Final != "done:after" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestNativeCancellationReapsExactAdapter(t *testing.T) {
	starter, request, binding := startHelper(t, "native-hang", sessionruntime.Options{})
	defer func() { _ = starter.Abort(context.Background(), request, binding) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := starter.NativeControl(ctx, request.SessionID, sessionruntime.NativeRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := starter.Wait(waitCtx, request.SessionID, binding); err != nil {
		t.Fatal(err)
	}
}

func TestNativeStaleReturnsCurrentScreenAndDoesNotRetire(t *testing.T) {
	starter, request, binding := startHelper(t, "native-stale", sessionruntime.Options{})
	defer func() { _ = starter.Abort(context.Background(), request, binding) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot, err := starter.NativeControl(ctx, request.SessionID, sessionruntime.NativeRequest{Key: "enter"})
	if !errors.Is(err, sessionruntime.ErrNativeStale) || snapshot.Text != "current screen" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if _, err := starter.Submit(ctx, request.SessionID, "after"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeWaitUnblocksOnConcurrentClose(t *testing.T) {
	starter, request, binding := startHelper(t, "native-hang", sessionruntime.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := starter.NativeControl(ctx, request.SessionID, sessionruntime.NativeRequest{})
		done <- err
	}()
	if err := starter.Abort(ctx, request, binding); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed process acknowledged control")
		}
	case <-ctx.Done():
		t.Fatal("native waiter did not exit")
	}
}

func TestNativeModelCacheFollowsExactLiveGeneration(t *testing.T) {
	starter, request, binding := startHelper(t, "native-model", sessionruntime.Options{})
	defer func() { _ = starter.Abort(context.Background(), request, binding) }()
	if model, ok := starter.NativeModel(request.SessionID); !ok || model != "startup-model" {
		t.Fatal("startup model not cached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := starter.NativeControl(ctx, request.SessionID, sessionruntime.NativeRequest{}); err != nil {
		t.Fatal(err)
	}
	if model, ok := starter.NativeModel(request.SessionID); !ok || model != "actual-model" {
		t.Fatal("snapshot model not cached")
	}
	if err := starter.Abort(ctx, request, binding); err != nil {
		t.Fatal(err)
	}
	if _, ok := starter.NativeModel(request.SessionID); ok {
		t.Fatal("retired model leaked through cache")
	}
	resumed := request
	resumed.Mode = "resume"
	resumed.PriorBinding = &binding
	next, err := starter.Start(ctx, resumed)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = starter.Abort(context.Background(), resumed, next) }()
	if model, ok := starter.NativeModel(request.SessionID); !ok || model != "startup-model" {
		t.Fatal("replacement generation retained old model")
	}
}
