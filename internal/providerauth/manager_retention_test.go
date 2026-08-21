package providerauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerExpiresCompletedFlowAfterStatusWindow(t *testing.T) {
	process := &testProcess{wait: make(chan struct{}), ok: true}
	manager, err := NewManager("node", testAuthorizer{}, testLauncher{process: process})
	if err != nil {
		t.Fatal(err)
	}
	manager.ttl = 25 * time.Millisecond
	status, err := manager.Start(context.Background(), StartRequest{
		ActorID: 7, NodeID: "node", Backend: BackendCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	close(process.wait)
	status = waitForState(t, manager, status, StateSucceeded)
	deadline := time.Now().Add(time.Second)
	for {
		_, statusErr := manager.Status(context.Background(), FlowRequest{
			ActorID: 7, NodeID: "node", FlowID: status.FlowID,
		})
		if errors.Is(statusErr, ErrFlowNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("terminal flow survived expiry: %v", statusErr)
		}
		time.Sleep(time.Millisecond)
	}
	manager.mu.Lock()
	remaining := len(manager.flows)
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("retained flows=%d", remaining)
	}
}
