// Package turnfailure classifies terminal provider outcomes without treating
// intentional interruption or service cancellation as provider failures.
package turnfailure

import (
	"context"
	"errors"

	"bria/internal/controllertelemetry"
	"bria/internal/sessionruntime"
)

func IsProviderFailure(result sessionruntime.TurnResult, err error, intentionalCancellation bool) bool {
	if result.TerminalStatus == sessionruntime.StatusFailed {
		return true
	}
	if result.TerminalStatus == sessionruntime.StatusInterrupted || result.ErrorCode == sessionruntime.ErrorInterrupted {
		return false
	}
	if err == nil {
		return false
	}
	return !intentionalCancellation || !cancellationOnly(err)
}

// cancellationOnly accepts wrappers around context.Canceled but rejects a
// joined provider failure. errors.Is alone is insufficient because it returns
// true for errors.Join(context.Canceled, providerErr).
func cancellationOnly(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return cancellationOnly(wrapped.Unwrap())
	}
	return errors.Is(err, context.Canceled)
}

func Reason(err error) controllertelemetry.Reason {
	if errors.Is(err, context.DeadlineExceeded) {
		return controllertelemetry.DeadlineExceeded
	}
	return controllertelemetry.RuntimeFailureReason(sessionruntime.RuntimeFailureClass(err))
}
