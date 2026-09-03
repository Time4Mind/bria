package executor_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/executor"
)

type handlerFunc func(context.Context, executor.Request) (executor.Response, error)

func (fn handlerFunc) Execute(ctx context.Context, request executor.Request) (executor.Response, error) {
	return fn(ctx, request)
}

func TestLocalAndRemoteExecutorsExposeSameContract(t *testing.T) {
	handler := handlerFunc(func(_ context.Context, request executor.Request) (executor.Response, error) {
		return executor.Response{OperationID: request.OperationID, Accepted: true}, nil
	})
	local, err := executor.NewLocal(handler)
	if err != nil {
		t.Fatal(err)
	}
	transport := executor.NewMemoryTransport()
	if err := transport.Register("computer-2", local); err != nil {
		t.Fatal(err)
	}
	remote, err := executor.NewRemote("computer-2", transport)
	if err != nil {
		t.Fatal(err)
	}
	request := executor.Request{OperationID: "operation-1", Generation: 1, SessionID: "session-1", Action: executor.ActionSubmit, Payload: []byte("hello")}

	for name, route := range map[string]executor.Executor{"local": local, "remote": remote} {
		t.Run(name, func(t *testing.T) {
			response, err := route.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !response.Accepted || response.OperationID != request.OperationID {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestRemoteExecutorReturnsOfflineForUnknownComputer(t *testing.T) {
	remote, err := executor.NewRemote("missing", executor.NewMemoryTransport())
	if err != nil {
		t.Fatal(err)
	}
	_, err = remote.Execute(context.Background(), executor.Request{OperationID: "operation-1", Generation: 1, SessionID: "session-1", Action: executor.ActionSubmit})
	if !errors.Is(err, executor.ErrComputerOffline) {
		t.Fatalf("Execute error = %v, want ErrComputerOffline", err)
	}
}
