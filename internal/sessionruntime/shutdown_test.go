//go:build darwin || linux

package sessionruntime_test

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"bria/internal/sessionruntime"
)

func TestShutdownWaitsForSeparateProviderTreeAndRejectsNewLaunch(t *testing.T) {
	starter := newTreeStarter(t, "cleanup", sessionruntime.Options{GracefulCloseTimeout: 30 * time.Millisecond, GracefulTerminateTimeout: time.Second})
	request := testRequest(t.TempDir(), "shutdown-cleanup")
	if _, err := starter.Start(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	rawPID := waitGrandchildPID(t, filepath.Join(request.Workdir, "raw.pid"))
	grandchildPID := waitGrandchildPID(t, filepath.Join(request.Workdir, "raw-grandchild.pid"))
	t.Cleanup(func() { _ = syscall.Kill(-rawPID, syscall.SIGKILL) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := starter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, rawPID)
	waitPIDGone(t, grandchildPID)
	if _, err := starter.Start(ctx, testRequest(t.TempDir(), "after-shutdown")); err == nil {
		t.Fatal("new adapter launched after shutdown")
	}
	if err := starter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
