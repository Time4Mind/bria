package main

import (
	"context"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

func (c *nodeRuntimeControl) Close() error {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Close runtime admission before waiting for HTTP keep-alives. Requests
	// already accepted are terminally drained while the servers shut down.
	c.executor.BeginShutdown()
	c.client.CloseIdleConnections()
	if c.descriptionClient != nil {
		c.descriptionClient.CloseIdleConnections()
	}
	var authErr error
	if c.localProviderAuth != nil {
		authErr = c.localProviderAuth.Close()
	}
	serverErr := c.server.Shutdown(ctx)
	enrollmentErr := closeEnrollmentRuntime(ctx, c.enrollment)
	executorErr := c.executor.Shutdown(ctx)
	if !c.executor.ShutdownComplete() {
		processlog.Failuref(
			processlog.Service, processlog.FailureTimeout,
			"bria runtime shutdown: outcome=timeout store_closed=false",
		)
	} else if executorErr != nil {
		processlog.Failuref(
			processlog.Service, processlog.FailureConsistency,
			"bria runtime shutdown: outcome=completion_failed workers_stopped=true",
		)
	}
	storeErr := closeRuntimeStoreAfterWorkers(c.executor, c.store)
	if storeErr != nil {
		processlog.Failuref(
			processlog.Service, processlog.FailureIO,
			"bria runtime shutdown: outcome=store_close_failed",
		)
	}
	return errors.Join(serverErr, enrollmentErr, authErr, executorErr, storeErr)
}

func closeRuntimeStoreAfterWorkers(
	executor *runtimehost.LocalExecutor,
	store *runtimehost.BoltOperationStore,
) error {
	if !executor.ShutdownComplete() {
		return nil
	}
	return store.Close()
}
