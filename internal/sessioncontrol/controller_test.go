package sessioncontrol

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type machinePort struct{ machine *clusterstate.Machine }

func (p machinePort) State() *domain.State { return p.machine.State() }
func (p machinePort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	return p.machine.Apply(command), nil
}

type runtimeStub struct {
	mu           sync.Mutex
	requests     []runtimehost.Request
	results      map[string]runtimehost.Result
	resultErrors map[string]error
	failures     int
	err          error
}

type clearCommitApplier struct {
	mu            sync.Mutex
	machine       *clusterstate.Machine
	failures      int
	unknownCommit bool
	attempts      int
	operationIDs  []string
}

func (p *clearCommitApplier) State() *domain.State { return p.machine.State() }

func (p *clearCommitApplier) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	if command.Kind != clusterstate.CommandClearSession {
		return p.machine.Apply(command), nil
	}
	p.mu.Lock()
	p.attempts++
	p.operationIDs = append(p.operationIDs, command.OperationID)
	fail := p.failures > 0
	if fail {
		p.failures--
	}
	unknown := fail && p.unknownCommit
	p.mu.Unlock()
	if !fail {
		return p.machine.Apply(command), nil
	}
	if unknown {
		result := p.machine.Apply(command)
		return result, errors.New("clear commit outcome is unknown")
	}
	return clusterstate.Result{}, errors.New("clear commit temporarily unavailable")
}

func (p *clearCommitApplier) snapshot() (int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.attempts, append([]string(nil), p.operationIDs...)
}

func (r *runtimeStub) Submit(
	_ context.Context,
	request runtimehost.Request,
) (runtimehost.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, request)
	if r.err != nil {
		return runtimehost.Receipt{}, r.err
	}
	if r.failures > 0 {
		r.failures--
		return runtimehost.Receipt{}, errors.New("node temporarily unavailable")
	}
	return runtimehost.Receipt{OperationID: request.OperationID, Accepted: true}, nil
}

func (r *runtimeStub) LookupResult(
	_ context.Context,
	request runtimehost.Request,
) (runtimehost.Result, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, ok := r.results[request.OperationID]
	return result, ok, r.resultErrors[request.OperationID]
}

func clearControllerFixture(
	t *testing.T,
	applier *clearCommitApplier,
) (*Controller, *runtimeStub, *clusterstate.Machine) {
	t.Helper()
	state := domain.NewState()
	if err := state.AddNode(domain.Node{ID: "node", Name: "Node", Status: domain.NodeOnline}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetNodeAccess(7, domain.RoleOwner, "node"); err != nil {
		t.Fatal(err)
	}
	if err := state.AddSession(domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Name: "Named", Backend: "claude",
		ProviderSessionID: "provider", State: domain.SessionLive,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 1,
		CreatedAt: time.Unix(1, 0), LiveSinceAt: time.Unix(1, 0),
	}); err != nil {
		t.Fatal(err)
	}
	machine := clusterstate.NewMachine(state)
	applier.machine = machine
	service, err := application.NewService(applier, applier)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeStub{
		results: make(map[string]runtimehost.Result), resultErrors: make(map[string]error),
	}
	controller, err := New(service, runtime)
	if err != nil {
		t.Fatal(err)
	}
	controller.pollInterval = time.Millisecond
	controller.retryInterval = time.Millisecond
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := controller.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return controller, runtime, machine
}

func TestSendInputPinsActiveSessionAndPublishesRunning(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	accepted, err := controller.SendInput(
		context.Background(), application.Principal{UserID: 7}, "send-1", "hello",
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Session.Key() != "node/session" {
		t.Fatalf("accepted session=%s", accepted.Session.Key())
	}
	runtime.mu.Lock()
	request := runtime.requests[0]
	runtime.mu.Unlock()
	if request.Text != "hello" || request.ExpectedGeneration != 1 {
		t.Fatalf("runtime request=%#v", request)
	}
	session := machine.State().Sessions["node/session"]
	if session.RuntimePhase != domain.RuntimeRunning || session.LastOperation == nil ||
		session.LastOperation.Status != domain.OperationQueued {
		t.Fatalf("replicated session=%#v", session)
	}
	if !session.UserRequestTracked || !session.UserRequestSeen {
		t.Fatalf("accepted request was not durably tracked: %#v", session)
	}
}

func TestPlainInputConfirmationKeepsTurnRunningAndUnconfirmedDegrades(t *testing.T) {
	for _, test := range []struct {
		name       string
		accepted   string
		delivered  bool
		resultErr  error
		wantPhase  domain.RuntimePhase
		wantStatus domain.OperationStatus
	}{
		{name: "confirmed", accepted: "true", delivered: true, wantPhase: domain.RuntimeRunning, wantStatus: domain.OperationQueued},
		{name: "pending", delivered: true, wantPhase: domain.RuntimeRunning, wantStatus: domain.OperationQueued},
		{name: "unconfirmed", accepted: "false", resultErr: runtimehost.ErrInputUnconfirmed, wantPhase: domain.RuntimeDegraded, wantStatus: domain.OperationFailed},
		{name: "rejected invariant", accepted: "false", delivered: true, wantPhase: domain.RuntimeDegraded, wantStatus: domain.OperationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, runtime, machine := controllerFixture(t)
			operationID := "plain-" + test.name
			if _, err := controller.SendInput(
				context.Background(), application.Principal{UserID: 7}, operationID, "prompt",
			); err != nil {
				t.Fatal(err)
			}
			var providerAccepted *bool
			if test.accepted != "" {
				accepted := test.accepted == "true"
				providerAccepted = &accepted
			}
			runtime.mu.Lock()
			runtime.results[operationID] = runtimehost.Result{
				Accepted: true, Delivered: test.delivered, ProviderAccepted: providerAccepted,
				Detail: "provider did not confirm submitted input",
			}
			runtime.resultErrors[operationID] = test.resultErr
			runtime.mu.Unlock()

			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				session := machine.State().Sessions["node/session"]
				if session.RuntimePhase == test.wantPhase && session.LastOperation != nil &&
					session.LastOperation.Status == test.wantStatus {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatalf("session=%+v", machine.State().Sessions["node/session"])
		})
	}
}

func TestExternalInputPinsDescriptorWithoutMediaBytes(t *testing.T) {
	controller, runtime, _ := controllerFixture(t)
	input := runtimehost.InputPayload{
		Kind: runtimehost.InputVoice,
		File: runtimehost.InputFile{
			Provider: "telegram", ID: "file-id", UniqueID: "unique-id", Size: 42,
		},
	}
	accepted, err := controller.SendExternalInput(
		context.Background(), application.Principal{UserID: 7}, "voice-1", input,
	)
	if err != nil || accepted.Session.Key() != "node/session" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	request := runtime.requests[0]
	if request.Text != "" || request.Input == nil || request.Input.File.ID != "file-id" {
		t.Fatalf("runtime request=%#v", request)
	}
}

func TestVoiceInputProviderConfirmationPendingIsNotFailure(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	input := runtimehost.InputPayload{
		Kind: runtimehost.InputVoice,
		File: runtimehost.InputFile{
			Provider: "telegram", ID: "voice-id", UniqueID: "voice-unique", Size: 42,
		},
		TranscriptBaselineKnown: true,
		TranscriptBaselineCount: 3,
		TranscriptOrdinal:       1,
	}
	const operationID = "voice-confirmation-pending"
	if _, err := controller.SendExternalInput(
		context.Background(), application.Principal{UserID: 7}, operationID, input,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results[operationID] = runtimehost.Result{
		Accepted: true, Delivered: true, Detail: runtimehost.ProviderConfirmationPendingDetail,
	}
	runtime.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session := machine.State().Sessions["node/session"]
		if session.RuntimePhase == domain.RuntimeRunning && session.LastOperation != nil &&
			session.LastOperation.Status == domain.OperationSucceeded &&
			session.LastOperation.Detail == runtimehost.ProviderConfirmationPendingDetail &&
			len(session.VoiceAcknowledgements) == 1 &&
			session.VoiceAcknowledgements[0].Status == domain.OperationSucceeded {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session=%+v", machine.State().Sessions["node/session"])
}

func TestExternalInputObservationOutlivesOrdinaryOperationDeadline(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	controller.resultWait = 5 * time.Millisecond
	controller.mediaResultWait = 250 * time.Millisecond
	input := runtimehost.InputPayload{
		Kind: runtimehost.InputVoice,
		File: runtimehost.InputFile{
			Provider: "telegram", ID: "voice-id", UniqueID: "voice-unique", Size: 42,
		},
	}
	if _, err := controller.SendExternalInput(
		context.Background(), application.Principal{UserID: 7}, "slow-voice", input,
	); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	runtime.mu.Lock()
	runtime.results["slow-voice"] = runtimehost.Result{
		Accepted: true, Delivered: false, Detail: "runtime operation failed",
	}
	runtime.mu.Unlock()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session := machine.State().Sessions["node/session"]
		if session.LastOperation != nil &&
			session.LastOperation.OperationID == "slow-voice" &&
			session.LastOperation.Status == domain.OperationFailed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("slow voice result was abandoned: %#v", machine.State().Sessions["node/session"])
}

func TestSendInputTimeoutEntersBoundedRetryWithoutBlockingAcceptance(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	runtime.mu.Lock()
	runtime.failures = 1
	runtime.mu.Unlock()
	accepted, err := controller.SendInput(
		context.Background(), application.Principal{UserID: 7}, "retry-input", "hello",
	)
	if err != nil || !accepted.Receipt.Accepted {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	if got := machine.State().Sessions["node/session"].RuntimePhase; got != domain.RuntimeRunning {
		t.Fatalf("phase after retry enqueue=%q", got)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		attempts := len(runtime.requests)
		runtime.mu.Unlock()
		if attempts >= 2 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime submission was not retried")
}

func TestSendInputReturnsQueueBackpressureWithoutRetrying(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	runtime.mu.Lock()
	runtime.err = runtimehost.ErrQueueFull
	runtime.mu.Unlock()
	accepted, err := controller.SendInput(
		context.Background(), application.Principal{UserID: 7}, "full-input", "hello",
	)
	if !errors.Is(err, domain.ErrQueueFull) || accepted.Session.Key() != "node/session" {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	time.Sleep(5 * time.Millisecond)
	runtime.mu.Lock()
	attempts := len(runtime.requests)
	runtime.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("queue-full submit attempts=%d, want 1", attempts)
	}
	if got := machine.State().Sessions["node/session"].RuntimePhase; got != domain.RuntimeIdle {
		t.Fatalf("phase after rejected input=%q", got)
	}
}

func TestCapturePaneUsesReadOnlyRuntimeFIFO(t *testing.T) {
	controller, runtime, _ := controllerFixture(t)
	runtime.mu.Lock()
	runtime.results["pane-1"] = runtimehost.Result{
		Accepted: true, Delivered: true, Pane: []byte("\x1b[32mready\x1b[0m"),
	}
	runtime.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pane, err := controller.CapturePane(
		ctx, application.Principal{UserID: 7}, "pane-1",
		domain.SessionRef{NodeID: "node", SessionID: "session"},
	)
	if err != nil || string(pane) != "\x1b[32mready\x1b[0m" {
		t.Fatalf("pane=%q err=%v", pane, err)
	}
}

func TestSuccessfulStopChangesStoppingToIdle(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	command, err := clusterstate.NewCommand(
		"mark-running", clusterstate.CommandPublishSessionRuntime, time.Now(),
		clusterstate.PublishSessionRuntime{
			Session: ref, Generation: 1, Phase: domain.RuntimeRunning,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result := machine.Apply(command); result.Err() != nil {
		t.Fatal(result.Err())
	}
	if _, err := controller.Stop(
		context.Background(), application.Principal{UserID: 7}, "stop-1", ref,
	); err != nil {
		t.Fatal(err)
	}
	if got := machine.State().Sessions[ref.Key()].RuntimePhase; got != domain.RuntimeStopping {
		t.Fatalf("phase after accept=%q", got)
	}
	runtime.mu.Lock()
	runtime.results["stop-1"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	waitForPhase(t, machine, ref, domain.RuntimeIdle)
}

func TestSuccessfulClearResetsNameAndProviderBinding(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Clear(
		context.Background(), application.Principal{UserID: 7}, "clear-1", ref,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results["clear-1"] = runtimehost.Result{
		Accepted: true, Delivered: true, ResetNaming: true, ResetProviderBinding: true,
	}
	runtime.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session := machine.State().Sessions[ref.Key()]
		if session.RuntimeGeneration == 2 {
			if session.Name != "" || session.ProviderSessionID != "" ||
				session.RuntimePhase != domain.RuntimeIdle {
				t.Fatalf("cleared session=%#v", session)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("clear result was not committed")
}

func TestClearCommitRetriesTransientFailureWithSameOperationScope(t *testing.T) {
	applier := &clearCommitApplier{failures: 1}
	controller, runtime, machine := clearControllerFixture(t, applier)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Clear(
		context.Background(), application.Principal{UserID: 7}, "clear-transient", ref,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results["clear-transient"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if machine.State().Sessions[ref.Key()].RuntimeGeneration == 2 {
			attempts, operationIDs := applier.snapshot()
			if attempts != 2 || len(operationIDs) != 2 || operationIDs[0] != operationIDs[1] {
				t.Fatalf("clear retries=%d operation_ids=%q", attempts, operationIDs)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("transient clear was not committed: %#v", machine.State().Sessions[ref.Key()])
}

func TestClearCommitTreatsUnknownOutcomeAsCommittedAfterReread(t *testing.T) {
	applier := &clearCommitApplier{failures: 1, unknownCommit: true}
	controller, runtime, machine := clearControllerFixture(t, applier)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Clear(
		context.Background(), application.Principal{UserID: 7}, "clear-unknown", ref,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results["clear-unknown"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if machine.State().Sessions[ref.Key()].RuntimeGeneration == 2 {
			attempts, _ := applier.snapshot()
			if attempts != 1 {
				t.Fatalf("unknown clear outcome was replayed: attempts=%d", attempts)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("unknown clear was not recognized: %#v", machine.State().Sessions[ref.Key()])
}

func TestClearCommitStopsAfterBoundedPermanentFailure(t *testing.T) {
	applier := &clearCommitApplier{failures: clearCommitMaxAttempts + 1}
	controller, runtime, machine := clearControllerFixture(t, applier)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Clear(
		context.Background(), application.Principal{UserID: 7}, "clear-permanent", ref,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results["clear-permanent"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		attempts, _ := applier.snapshot()
		if attempts == clearCommitMaxAttempts {
			break
		}
		time.Sleep(time.Millisecond)
	}
	attempts, _ := applier.snapshot()
	if attempts != clearCommitMaxAttempts {
		t.Fatalf("permanent clear attempts=%d, want %d", attempts, clearCommitMaxAttempts)
	}
	time.Sleep(10 * time.Millisecond)
	if got, _ := applier.snapshot(); got != attempts {
		t.Fatalf("permanent clear kept retrying: attempts=%d then=%d", attempts, got)
	}
	got := machine.State().Sessions[ref.Key()]
	if got.RuntimeGeneration != 1 || got.RuntimePhase != domain.RuntimeStopping {
		t.Fatalf("permanent clear changed state despite failed commit: %#v", got)
	}
}

func TestCloseCommitsArchiveIdentityBeforeRuntimeDeactivation(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	accepted, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "close-1", ref,
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Session != ref {
		t.Fatalf("accepted ref=%v", accepted.Session)
	}
	session := machine.State().Sessions[ref.Key()]
	if session.State != domain.SessionArchived || session.ArchiveID != "archive-close-1" {
		t.Fatalf("closed session=%#v", session)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	request := runtime.requests[len(runtime.requests)-1]
	if request.Action != runtimehost.ActionClose || request.ArchiveCommitID != session.ArchiveID {
		t.Fatalf("close request=%#v", request)
	}
}

func TestCloseReportsDeferredRuntimeDeactivation(t *testing.T) {
	controller, runtime, _ := controllerFixture(t)
	runtime.mu.Lock()
	runtime.failures = 1
	runtime.mu.Unlock()
	accepted, err := controller.Close(
		context.Background(), application.Principal{UserID: 7}, "close-retry",
		domain.SessionRef{NodeID: "node", SessionID: "session"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Deferred {
		t.Fatal("deferred runtime deactivation was not reported to the caller")
	}
}
