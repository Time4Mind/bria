package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/runtimehost"
)

type shutdownBlockingDriver struct {
	started chan struct{}
	release chan struct{}
}

func (d *shutdownBlockingDriver) SendLiteral(context.Context, string, string, string) error {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-d.release
	return nil
}

func (*shutdownBlockingDriver) SendKey(context.Context, string, string) error { return nil }
func (*shutdownBlockingDriver) Close(context.Context, string) error           { return nil }
func (*shutdownBlockingDriver) OpenTerminal(context.Context, string) error    { return nil }
func (*shutdownBlockingDriver) CapturePane(context.Context, string) ([]byte, error) {
	return nil, nil
}

func TestRuntimeStoreRemainsOpenUntilShutdownWorkersFinish(t *testing.T) {
	store, err := runtimehost.OpenBoltOperationStore(filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	driver := &shutdownBlockingDriver{started: make(chan struct{}, 1), release: make(chan struct{})}
	executor, err := runtimehost.NewLocalExecutor("node", driver, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.Register(runtimehost.RuntimeBinding{
		NodeID: "node", SessionID: "session", Generation: 1,
		TmuxTarget: "@1", Backend: "codex",
	}); err != nil {
		t.Fatal(err)
	}
	request := runtimehost.Request{
		OperationID: "blocked", ActorID: 1, NodeID: "node", SessionID: "session",
		ExpectedGeneration: 1, Action: runtimehost.ActionSendInput,
		Text: "input", Backend: "codex",
	}
	if _, err := executor.Submit(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.started:
	case <-time.After(time.Second):
		t.Fatal("runtime operation did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := executor.Shutdown(ctx); err == nil {
		t.Fatal("blocked worker unexpectedly completed before timeout")
	}
	if err := closeRuntimeStoreAfterWorkers(executor, store); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lookup(request.OperationID); err != nil || !found {
		t.Fatalf("store closed under worker found=%t err=%v", found, err)
	}

	close(driver.release)
	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := closeRuntimeStoreAfterWorkers(executor, store); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Lookup(request.OperationID); err == nil {
		t.Fatal("store remained open after workers finished")
	}
}
