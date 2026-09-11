// Package durableoutputwait observes one exact output until its durable
// delivery reaches a terminal phase, without leasing or mutating the journal.
package durableoutputwait

import (
	"context"
	"errors"
	"time"

	"bria/internal/messagejournal"
)

type Reader interface {
	Outputs(context.Context, string) ([]messagejournal.Output, error)
}

func Wait(ctx context.Context, reader Reader, sessionID, operationID string) error {
	if reader == nil || sessionID == "" || operationID == "" {
		return errors.New("durable output wait identity is invalid")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		outputs, err := reader.Outputs(ctx, sessionID)
		if err != nil {
			return err
		}
		found := false
		for _, output := range outputs {
			if output.OperationID != operationID {
				continue
			}
			found = true
			switch output.Phase {
			case messagejournal.OutputConfirmed:
				return nil
			case messagejournal.OutputFailed, messagejournal.OutputUnknown, messagejournal.OutputSuperseded:
				return errors.New("durable output delivery did not confirm")
			case messagejournal.OutputPending:
			default:
				return errors.New("durable output delivery phase is invalid")
			}
			break
		}
		if !found {
			return messagejournal.ErrNotFound
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
