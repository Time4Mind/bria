package viewdeliverycontext_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/viewdeliverycontext"
)

func TestCaptureObservesNavigationSynchronouslyAndPreservesParentValues(t *testing.T) {
	type key struct{}
	parent := context.WithValue(context.Background(), key{}, "operation")
	view, stop := context.WithCancel(context.Background())
	ctx, cleanup := viewdeliverycontext.Capture(parent, view, context.Background())
	defer cleanup()
	stop()
	if ctx.Err() != context.Canceled || context.Cause(ctx) != context.Canceled {
		t.Fatal("navigation was not immediately observable")
	}
	if ctx.Value(key{}) != "operation" {
		t.Fatal("delivery lost parent correlation values")
	}
	child, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	if child.Err() != context.Canceled {
		t.Fatal("derived transport context missed cancellation")
	}
}

func TestCaptureDistinguishesShutdownIncludingAlreadyStoppedRoot(t *testing.T) {
	for _, before := range []bool{false, true} {
		root, stop := context.WithCancel(context.Background())
		view, stopView := context.WithCancel(root)
		if before {
			stop()
		}
		ctx, cleanup := viewdeliverycontext.Capture(context.Background(), view, root)
		stop()
		if ctx.Err() != context.Canceled || context.Cause(ctx) == nil || context.Cause(ctx) == context.Canceled {
			t.Fatal("shutdown became navigation suppression")
		}
		cleanup()
		stopView()
	}
}

func TestCapturePreservesParentCancellationCause(t *testing.T) {
	parent, stop := context.WithCancelCause(context.Background())
	failure := errors.New("synthetic caller failure")
	ctx, cleanup := viewdeliverycontext.Capture(parent, context.Background(), context.Background())
	defer cleanup()
	stop(failure)
	if context.Cause(ctx) != failure {
		t.Fatal("caller cancellation cause lost")
	}
}

func TestRenewCancelsPreviousViewWithoutMakingHiddenViewVisible(t *testing.T) {
	old, stop := context.WithCancel(context.Background())
	view, cancel := viewdeliverycontext.Renew(context.Background(), stop, false)
	if old.Err() != context.Canceled || view != nil || cancel != nil {
		t.Fatal("hidden replacement retained a delivery view")
	}
	view, cancel = viewdeliverycontext.Renew(context.Background(), nil, true)
	if view == nil || view.Err() != nil || cancel == nil {
		t.Fatal("visible replacement missing")
	}
	cancel()
	if view.Err() != context.Canceled {
		t.Fatal("renewed view is not cancellable")
	}
}
