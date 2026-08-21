package providerauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type testAuthorizer struct{ err error }

func (a testAuthorizer) AuthorizeProviderAuth(context.Context, int64, string, string) error {
	return a.err
}

type testLauncher struct {
	process *testProcess
	err     error
}

func (l testLauncher) Launch(context.Context, string, string) (Process, error) {
	return l.process, l.err
}

type testProcess struct {
	url, code string
	input     bool
	wait      chan struct{}
	ok        bool
	detail    string
	submitErr error
	mu        sync.Mutex
	submitted string
	cancelled bool
}

func (p *testProcess) Challenge() (string, string, bool) { return p.url, p.code, p.input }
func (p *testProcess) Submit(_ context.Context, code string) error {
	p.mu.Lock()
	p.submitted = code
	p.mu.Unlock()
	return p.submitErr
}
func (p *testProcess) Wait() (bool, string) { <-p.wait; return p.ok, p.detail }
func (p *testProcess) Cancel() error {
	p.mu.Lock()
	p.cancelled = true
	p.mu.Unlock()
	return nil
}

func TestManagerClaudeFlowIsActorBoundAndCompletes(t *testing.T) {
	process := &testProcess{
		url: "https://claude.com/auth", input: true, wait: make(chan struct{}), ok: true,
	}
	manager, err := NewManager("node", testAuthorizer{}, testLauncher{process: process})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Start(context.Background(), StartRequest{
		ActorID: 7, NodeID: "node", Backend: BackendClaude,
	})
	if err != nil || status.State != StateWaitingInput || status.URL != process.url {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err := manager.Status(context.Background(), FlowRequest{
		ActorID: 8, NodeID: "node", FlowID: status.FlowID,
	}); !errors.Is(err, ErrFlowNotFound) {
		t.Fatalf("cross-actor status err=%v", err)
	}
	if _, err := manager.Submit(context.Background(), SubmitRequest{
		ActorID: 7, NodeID: "node", FlowID: status.FlowID, Code: "secret-code",
	}); err != nil {
		t.Fatal(err)
	}
	close(process.wait)
	status = waitForState(t, manager, status, StateSucceeded)
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.submitted != "secret-code" {
		t.Fatalf("submitted=%q", process.submitted)
	}
}

func TestManagerRejectsUnauthorizedAndCancelsReplacedFlow(t *testing.T) {
	denied, err := NewManager("node", testAuthorizer{err: errors.New("denied")},
		testLauncher{process: &testProcess{wait: make(chan struct{})}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Start(context.Background(), StartRequest{
		ActorID: 7, NodeID: "node", Backend: BackendCodex,
	}); err == nil {
		t.Fatal("unauthorized start succeeded")
	}

	first := &testProcess{wait: make(chan struct{})}
	second := &testProcess{wait: make(chan struct{})}
	launcher := &sequenceLauncher{processes: []*testProcess{first, second}}
	manager, err := NewManager("node", testAuthorizer{}, launcher)
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{ActorID: 7, NodeID: "node", Backend: BackendCodex}
	if _, err := manager.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		first.mu.Lock()
		cancelled := first.cancelled
		first.mu.Unlock()
		if cancelled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("replaced flow was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagerRetainsCancelledFlowForImmediateStatus(t *testing.T) {
	process := &testProcess{wait: make(chan struct{})}
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
	if err := manager.Cancel(context.Background(), FlowRequest{
		ActorID: 7, NodeID: "node", FlowID: status.FlowID,
	}); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status(context.Background(), FlowRequest{
		ActorID: 7, NodeID: "node", FlowID: status.FlowID,
	})
	if err != nil || status.State != StateCancelled {
		t.Fatalf("cancelled status=%+v err=%v", status, err)
	}
	waitForNotFound(t, manager, status)
	close(process.wait)
}

func TestManagerRetainsFailedFlowForImmediateStatus(t *testing.T) {
	process := &testProcess{
		wait: make(chan struct{}), input: true,
		submitErr: errors.New("rejected"),
	}
	manager, err := NewManager("node", testAuthorizer{}, testLauncher{process: process})
	if err != nil {
		t.Fatal(err)
	}
	manager.ttl = 25 * time.Millisecond
	status, err := manager.Start(context.Background(), StartRequest{
		ActorID: 7, NodeID: "node", Backend: BackendClaude,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Submit(context.Background(), SubmitRequest{
		ActorID: 7, NodeID: "node", FlowID: status.FlowID, Code: "secret-code",
	})
	if err == nil {
		t.Fatal("rejected submit succeeded")
	}
	status, statusErr := manager.Status(context.Background(), FlowRequest{
		ActorID: 7, NodeID: "node", FlowID: status.FlowID,
	})
	if statusErr != nil || status.State != StateFailed {
		t.Fatalf("failed status=%+v err=%v", status, statusErr)
	}
	waitForNotFound(t, manager, status)
	close(process.wait)
}

func TestManagerPendingFlowStillExpiresAsBefore(t *testing.T) {
	process := &testProcess{wait: make(chan struct{})}
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
	if status.State != StateWaitingUser {
		t.Fatalf("initial status=%+v", status)
	}
	if current, err := manager.Status(context.Background(), FlowRequest{
		ActorID: 7, NodeID: "node", FlowID: status.FlowID,
	}); err != nil || current.State != StateWaitingUser {
		t.Fatalf("pending status=%+v err=%v", current, err)
	}
	waitForNotFound(t, manager, status)
	waitForProcessCancellation(t, process)
	close(process.wait)
}

func TestManagerConcurrentStatusAndCancel(t *testing.T) {
	process := &testProcess{wait: make(chan struct{})}
	manager, err := NewManager("node", testAuthorizer{}, testLauncher{process: process})
	if err != nil {
		t.Fatal(err)
	}
	status, err := manager.Start(context.Background(), StartRequest{
		ActorID: 7, NodeID: "node", Backend: BackendCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := FlowRequest{ActorID: 7, NodeID: "node", FlowID: status.FlowID}
	var group sync.WaitGroup
	for index := 0; index < 16; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = manager.Status(context.Background(), request)
		}()
	}
	group.Add(1)
	go func() {
		defer group.Done()
		_ = manager.Cancel(context.Background(), request)
	}()
	group.Wait()
	close(process.wait)
}

func waitForNotFound(t *testing.T, manager *Manager, current Status) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		_, err := manager.Status(context.Background(), FlowRequest{
			ActorID: 7, NodeID: "node", FlowID: current.FlowID,
		})
		if errors.Is(err, ErrFlowNotFound) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("flow was not cleaned up: err=%v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForState(t *testing.T, manager *Manager, current Status, want State) Status {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		status, err := manager.Status(context.Background(), FlowRequest{
			ActorID: 7, NodeID: "node", FlowID: current.FlowID,
		})
		if err == nil && status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("flow did not complete: status=%+v err=%v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForProcessCancellation(t *testing.T, process *testProcess) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		process.mu.Lock()
		cancelled := process.cancelled
		process.mu.Unlock()
		if cancelled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expired pending process was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
}

type sequenceLauncher struct {
	mu        sync.Mutex
	processes []*testProcess
}

func (l *sequenceLauncher) Launch(context.Context, string, string) (Process, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	process := l.processes[0]
	l.processes = l.processes[1:]
	return process, nil
}
