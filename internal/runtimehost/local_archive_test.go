package runtimehost

import (
	"context"
	"testing"
	"time"
)

type fakeArchiveWriter struct{ calls int }

func (w *fakeArchiveWriter) Commit(context.Context, Request) error {
	w.calls++
	return nil
}

func (w *fakeArchiveWriter) Finalize(context.Context, Request) error {
	w.calls++
	return nil
}

func TestLocalExecutorCloseRequiresArchiveCommit(t *testing.T) {
	driver := &fakeRuntimeDriver{}
	executor := newTestExecutor(t, driver)
	request := testRequest("close-1", ActionClose)
	if _, err := executor.Submit(context.Background(), request); err == nil {
		t.Fatal("close without archive commit unexpectedly succeeded")
	}
	request.ArchiveCommitID = "archive-commit-7"
	request.Archive = &ArchivePayload{
		ArchiveID: request.ArchiveCommitID, OwnerID: 42, Workdir: "/tmp/project",
		ProviderSessionID: "provider", CreatedAt: time.Unix(1, 0),
		ArchivedAt: time.Unix(2, 0),
	}
	writer := &fakeArchiveWriter{}
	executor.SetArchiveWriter(writer)
	result := waitSubmittedResult(t, executor, request)
	if !result.Delivered {
		t.Fatalf("close result = %+v", result)
	}
	calls := driver.snapshot()
	if len(calls) != 1 || calls[0].action != "close" {
		t.Fatalf("close calls = %#v", calls)
	}
	if writer.calls != 2 || !result.ArchiveCommitted {
		t.Fatalf("archive calls=%d result=%+v", writer.calls, result)
	}
}
