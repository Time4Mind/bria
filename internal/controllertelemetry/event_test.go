package controllertelemetry_test

import (
	"context"
	"testing"

	"bria/internal/controllertelemetry"
)

func TestOperationContextRetainsInitiatingCorrelationWithoutCancellation(t *testing.T) {
	root := controllertelemetry.WithOperation(context.Background(), "close-one")
	ctx, cancel := context.WithCancel(root)
	cancel()
	deferred := context.WithoutCancel(ctx)
	other := controllertelemetry.WithOperation(root, "manual-select-two")
	if controllertelemetry.Operation(deferred) != "close-one" || controllertelemetry.Operation(other) != "manual-select-two" || controllertelemetry.Operation(root) != "close-one" || controllertelemetry.Operation(context.Background()) != "" {
		t.Fatal("operation context leaked across independent actions")
	}
}
