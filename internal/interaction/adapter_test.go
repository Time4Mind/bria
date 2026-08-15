package interaction

import (
	"context"
	"errors"
	"testing"
)

func TestStartReportsAdapterIdentity(t *testing.T) {
	want := errors.New("stopped")
	failures := Start(context.Background(), Func{
		AdapterName: "test-ui",
		RunFunc:     func(context.Context) error { return want },
	})
	failure := <-failures
	if failure.Adapter != "test-ui" || !errors.Is(failure.Err, want) {
		t.Fatalf("failure=%+v", failure)
	}
}

func TestStartWithoutAdaptersIsDisabled(t *testing.T) {
	if failures := Start(context.Background()); failures != nil {
		t.Fatal("empty adapter set returned a live error channel")
	}
}
