package sessioncontrol

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

type namingTranscriptStub struct {
	mu     sync.Mutex
	events []transcript.Event
}

func (s *namingTranscriptStub) ReadTranscript(
	context.Context,
	nodecontrol.TranscriptQuery,
) ([]transcript.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]transcript.Event(nil), s.events...), nil
}

func TestFirstInputAfterClearAssignsANewGeneratedName(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	if _, err := controller.Clear(
		context.Background(), application.Principal{UserID: 7}, "clear-for-name", ref,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results["clear-for-name"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	waitForGeneration(t, machine, ref, 2)

	runtime.mu.Lock()
	runtime.results["prompt-name"] = runtimehost.Result{
		Accepted: true, Delivered: true, GeneratedName: "archive repair",
	}
	runtime.mu.Unlock()
	if _, err := controller.SendInput(
		context.Background(), application.Principal{UserID: 7},
		"prompt", "Repair the archive restoration flow",
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if machine.State().Sessions[ref.Key()].Name == "archive repair" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := machine.State().Sessions[ref.Key()].Name; got != "archive repair" {
		t.Fatalf("generated name=%q", got)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	last := runtime.requests[len(runtime.requests)-1]
	if last.Action != runtimehost.ActionGenerateName || last.Text == "" ||
		last.ExpectedGeneration != 2 {
		t.Fatalf("naming request=%#v", last)
	}
}

func TestFailedNamingCallRetriesWithoutAnotherSessionInput(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	clearSessionForNaming(t, controller, runtime, machine, ref)
	runtime.mu.Lock()
	runtime.results["prompt-name"] = runtimehost.Result{Accepted: true, Delivered: false}
	runtime.results["prompt-name-retry-1"] = runtimehost.Result{
		Accepted: true, Delivered: true, GeneratedName: "retry-success",
	}
	runtime.mu.Unlock()
	if _, err := controller.SendInput(
		context.Background(), application.Principal{UserID: 7}, "prompt", "first request",
	); err != nil {
		t.Fatal(err)
	}
	waitForName(t, machine, ref, "retry-success")
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if got := runtime.requests[len(runtime.requests)-1]; got.OperationID != "prompt-name-retry-1" || got.Action != runtimehost.ActionGenerateName {
		t.Fatalf("retry request=%#v", got)
	}
}

func TestEnsureNameRecoversFirstTranscriptPromptWithoutResendingIt(t *testing.T) {
	controller, runtime, machine := controllerFixture(t)
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	clearSessionForNaming(t, controller, runtime, machine, ref)
	controller.transcripts = &namingTranscriptStub{events: []transcript.Event{
		{Kind: transcript.EventUserText, Text: "first persisted prompt"},
		{Kind: transcript.EventAssistantFinal, Text: "answer"},
	}}
	if !controller.EnsureName(application.Principal{UserID: 7}, ref) {
		t.Fatal("name recovery was not scheduled")
	}
	var namingRequest runtimehost.Request
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		if len(runtime.requests) > 1 {
			namingRequest = runtime.requests[len(runtime.requests)-1]
		}
		runtime.mu.Unlock()
		if namingRequest.OperationID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if namingRequest.Action != runtimehost.ActionGenerateName ||
		namingRequest.Text != "first persisted prompt" {
		t.Fatalf("recovery request=%#v", namingRequest)
	}
	runtime.mu.Lock()
	runtime.results[namingRequest.OperationID] = runtimehost.Result{
		Accepted: true, Delivered: true, GeneratedName: "persisted-prompt",
	}
	runtime.mu.Unlock()
	waitForName(t, machine, ref, "persisted-prompt")
	// Clear plus naming only: the recovery path did not enqueue send_input.
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	for _, request := range runtime.requests[1:] {
		if request.Action == runtimehost.ActionSendInput {
			t.Fatalf("prompt was resent to the interactive session: %#v", request)
		}
	}
}

func clearSessionForNaming(
	t *testing.T,
	controller *Controller,
	runtime *runtimeStub,
	machine interface{ State() *domain.State },
	ref domain.SessionRef,
) {
	t.Helper()
	if _, err := controller.Clear(
		context.Background(), application.Principal{UserID: 7}, "clear-for-recovery", ref,
	); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.results["clear-for-recovery"] = runtimehost.Result{Accepted: true, Delivered: true}
	runtime.mu.Unlock()
	waitForGeneration(t, machine, ref, 2)
}

func waitForName(
	t *testing.T,
	machine interface{ State() *domain.State },
	ref domain.SessionRef,
	want string,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if machine.State().Sessions[ref.Key()].Name == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("session name did not become %q", want)
}

func waitForGeneration(
	t *testing.T,
	machine interface{ State() *domain.State },
	ref domain.SessionRef,
	want uint64,
) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if machine.State().Sessions[ref.Key()].RuntimeGeneration == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime generation did not become %d", want)
}
