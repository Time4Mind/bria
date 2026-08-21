package runtimehost

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSuccessfulCloseRetiresLocalRuntimeWorker(t *testing.T) {
	executor := newTestExecutor(t, &fakeRuntimeDriver{})
	executor.SetArchiveWriter(&fakeArchiveWriter{})
	request := testRequest("close-retire", ActionClose)
	request.ArchiveCommitID = "archive-close-retire"
	request.Archive = &ArchivePayload{
		ArchiveID: request.ArchiveCommitID, OwnerID: 42, Workdir: "/work",
		ProviderSessionID: "provider", CreatedAt: time.Unix(1, 0).UTC(),
		ArchivedAt: time.Unix(2, 0).UTC(),
	}
	result := waitSubmittedResult(t, executor, request)
	if !result.Delivered || !result.ArchiveCommitted {
		t.Fatalf("close result=%+v", result)
	}

	deadline := time.Now().Add(time.Second)
	for {
		executor.mu.RLock()
		_, exists := executor.sessions[runtimeKey("node-a", "session-a")]
		executor.mu.RUnlock()
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed runtime remained registered")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := executor.Submit(context.Background(), testRequest("after-close", ActionStop)); !errors.Is(err, ErrRuntimeUnavailable) {
		t.Fatalf("operation after close error=%v", err)
	}
}

func TestSuccessfulDiscardClosesWithoutArchiveAndRetiresRuntime(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	result := waitSubmittedResult(t, executor, testRequest("discard-empty", ActionDiscard))
	if !result.Delivered || result.ArchiveCommitted {
		t.Fatalf("discard result=%+v", result)
	}
	calls := driver.snapshot()
	if len(calls) != 1 || calls[0].action != "close" {
		t.Fatalf("discard calls=%#v", calls)
	}
	executor.mu.RLock()
	_, exists := executor.sessions[runtimeKey("node-a", "session-a")]
	executor.mu.RUnlock()
	if exists {
		t.Fatal("discarded runtime remained registered")
	}
}
