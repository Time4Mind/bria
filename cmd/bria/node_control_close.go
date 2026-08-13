package main

import (
	"context"
	"errors"
	"time"
)

func (c *nodeRuntimeControl) Close() error {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.client.CloseIdleConnections()
	var authErr error
	if c.localProviderAuth != nil {
		authErr = c.localProviderAuth.Close()
	}
	return errors.Join(
		c.server.Shutdown(ctx), closeEnrollmentRuntime(ctx, c.enrollment),
		authErr, c.executor.Shutdown(ctx), c.store.Close(),
	)
}
