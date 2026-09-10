package promptpreprocesssession

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"bria/internal/promptpreprocess"
)

type poolFixtureSession struct {
	id             int
	processStarted chan<- int
	release        <-chan struct{}
	closed         chan<- int
}

type blockingCloseSession struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (*blockingCloseSession) Process(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
	return promptpreprocess.Result{}, nil
}

func (session *blockingCloseSession) Close(context.Context) error {
	close(session.started)
	<-session.release
	return nil
}

func (session *poolFixtureSession) Process(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
	session.processStarted <- session.id
	<-session.release
	return promptpreprocess.Result{Text: fmt.Sprintf("result-%d", session.id)}, nil
}

func (session *poolFixtureSession) Close(context.Context) error {
	session.closed <- session.id
	return nil
}

func TestPreprocessingPoolReplacesLeasedSessionAndClosesItOnlyAfterAcceptance(t *testing.T) {
	started := make(chan int, 4)
	processStarted := make(chan int, 1)
	closed := make(chan int, 4)
	release := make(chan struct{})
	var mu sync.Mutex
	next := 0
	pool := newSessionPool(func(context.Context) (session, error) {
		mu.Lock()
		next++
		id := next
		mu.Unlock()
		started <- id
		return &poolFixtureSession{id: id, processStarted: processStarted, release: release, closed: closed}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer pool.Close(context.Background())

	pool.Warmup(ctx)
	if got := receiveInt(t, started); got != 1 {
		t.Fatalf("warm session = %d, want 1", got)
	}
	type outcome struct {
		result     promptpreprocess.Result
		completion promptpreprocess.Completion
		err        error
	}
	done := make(chan outcome, 1)
	go func() {
		result, completion, err := pool.Process(ctx, promptpreprocess.Request{MessageID: "message-1"})
		done <- outcome{result: result, completion: completion, err: err}
	}()
	if got := receiveInt(t, processStarted); got != 1 {
		t.Fatalf("leased session = %d, want 1", got)
	}
	if got := receiveInt(t, started); got != 2 {
		t.Fatalf("replacement session = %d, want 2", got)
	}
	select {
	case got := <-closed:
		t.Fatalf("session %d closed before its result was accepted", got)
	default:
	}
	close(release)
	completed := <-done
	if completed.err != nil || completed.result.Text != "result-1" || completed.completion == nil {
		t.Fatalf("process outcome = (%#v, %T, %v)", completed.result, completed.completion, completed.err)
	}
	select {
	case got := <-closed:
		t.Fatalf("session %d closed before durable acceptance", got)
	default:
	}
	if err := completed.completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := receiveInt(t, closed); got != 1 {
		t.Fatalf("accepted session close = %d, want 1", got)
	}
}

func TestPreprocessingPoolReplaysUnacceptedOutcomeWithoutSecondModelTurn(t *testing.T) {
	started := make(chan int, 4)
	processStarted := make(chan int, 4)
	closed := make(chan int, 4)
	release := make(chan struct{})
	close(release)
	next := 0
	pool := newSessionPool(func(context.Context) (session, error) {
		next++
		started <- next
		return &poolFixtureSession{id: next, processStarted: processStarted, release: release, closed: closed}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer pool.Close(context.Background())
	request := promptpreprocess.Request{ComputerID: "local", SessionID: "session", MessageID: "message-1", Instruction: "clean", Text: "raw"}

	pool.Warmup(ctx)
	receiveInt(t, started)
	first, firstCompletion, err := pool.Process(ctx, request)
	if err != nil || first.Text != "result-1" || firstCompletion == nil || receiveInt(t, processStarted) != 1 {
		t.Fatalf("first outcome = (%#v, %T, %v)", first, firstCompletion, err)
	}
	receiveInt(t, started) // replacement is ready, but must not receive this request.
	second, secondCompletion, err := pool.Process(ctx, request)
	if err != nil || second.Text != first.Text || secondCompletion == nil {
		t.Fatalf("replayed outcome = (%#v, %T, %v)", second, secondCompletion, err)
	}
	select {
	case id := <-processStarted:
		t.Fatalf("unaccepted durable retry reached session %d", id)
	default:
	}
	if err := secondCompletion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := receiveInt(t, closed); got != 1 {
		t.Fatalf("accepted replay closed session %d, want 1", got)
	}
	if err := firstCompletion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-closed:
		t.Fatalf("session %d closed more than once", id)
	default:
	}
}

func TestPreprocessingPoolInvalidationDiscardsAStartingOldGeneration(t *testing.T) {
	started := make(chan int, 4)
	closed := make(chan int, 4)
	firstRelease := make(chan struct{})
	next := 0
	pool := newSessionPool(func(context.Context) (session, error) {
		next++
		id := next
		started <- id
		if id == 1 {
			<-firstRelease
		}
		return &poolFixtureSession{id: id, processStarted: make(chan int, 1), release: make(chan struct{}), closed: closed}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer pool.Close(context.Background())
	pool.Warmup(ctx)
	if got := receiveInt(t, started); got != 1 {
		t.Fatalf("starting generation = %d, want 1", got)
	}
	pool.Invalidate()
	close(firstRelease)
	if got := receiveInt(t, closed); got != 1 {
		t.Fatalf("stale generation close = %d, want 1", got)
	}
	if got := receiveInt(t, started); got != 2 {
		t.Fatalf("replacement generation = %d, want 2", got)
	}
}

func TestPreprocessingPoolShutdownWaitsForStartedSessionCleanup(t *testing.T) {
	factoryStarted := make(chan struct{})
	releaseFactory := make(chan struct{})
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	pool := newSessionPool(func(context.Context) (session, error) {
		close(factoryStarted)
		<-releaseFactory
		return &blockingCloseSession{started: closeStarted, release: releaseClose}, nil
	})
	pool.Warmup(context.Background())
	<-factoryStarted
	closed := make(chan error, 1)
	go func() { closed <- pool.Close(context.Background()) }()
	close(releaseFactory)
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("started session was not closed during shutdown")
	}
	select {
	case err := <-closed:
		t.Fatalf("pool shutdown returned before session cleanup: %v", err)
	default:
	}
	close(releaseClose)
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestPreprocessingPoolSerializesDifferentRequestsWhileReplacementWaitsReady(t *testing.T) {
	started := make(chan int, 4)
	processStarted := make(chan int, 4)
	closed := make(chan int, 4)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	next := 0
	pool := newSessionPool(func(context.Context) (session, error) {
		next++
		id := next
		started <- id
		return &poolFixtureSession{id: id, processStarted: processStarted, release: releases[id-1], closed: closed}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer pool.Close(context.Background())
	pool.Warmup(ctx)
	receiveInt(t, started)
	type outcome struct {
		completion promptpreprocess.Completion
		err        error
	}
	firstDone, secondDone := make(chan outcome, 1), make(chan outcome, 1)
	go func() {
		_, completion, err := pool.Process(ctx, promptpreprocess.Request{MessageID: "first", Instruction: "clean", Text: "one"})
		firstDone <- outcome{completion: completion, err: err}
	}()
	if receiveInt(t, processStarted) != 1 || receiveInt(t, started) != 2 {
		t.Fatal("first lease did not start one ready replacement")
	}
	go func() {
		_, completion, err := pool.Process(ctx, promptpreprocess.Request{MessageID: "second", Instruction: "clean", Text: "two"})
		secondDone <- outcome{completion: completion, err: err}
	}()
	select {
	case id := <-processStarted:
		t.Fatalf("second request reached ready session %d before first acceptance", id)
	case <-time.After(20 * time.Millisecond):
	}
	close(releases[0])
	first := <-firstDone
	if first.err != nil || first.completion == nil {
		t.Fatalf("first outcome = %#v", first)
	}
	select {
	case id := <-processStarted:
		t.Fatalf("second request reached session %d before durable acceptance", id)
	case <-time.After(20 * time.Millisecond):
	}
	if err := first.completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if receiveInt(t, closed) != 1 || receiveInt(t, processStarted) != 2 || receiveInt(t, started) != 3 {
		t.Fatal("acceptance did not hand the ready replacement to the next request")
	}
	close(releases[1])
	second := <-secondDone
	if second.err != nil || second.completion == nil {
		t.Fatalf("second outcome = %#v", second)
	}
	if err := second.completion.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	if receiveInt(t, closed) != 2 {
		t.Fatal("second session was not retired")
	}
}

func receiveInt(t *testing.T, values <-chan int) int {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lifecycle event")
		return 0
	}
}
