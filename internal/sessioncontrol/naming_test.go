package sessioncontrol

import (
	"context"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

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
