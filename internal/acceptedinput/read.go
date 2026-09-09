// Package acceptedinput verifies and commits exact durable acceptance custody.
package acceptedinput

import (
	"context"
	"errors"
	"strings"

	"bria/internal/messagejournal"
)

var ErrInvalidReceipt = errors.New("invalid provider hand-off result")

type JournalReader interface {
	Inputs(context.Context, string) ([]messagejournal.Input, error)
}

// Lookup validates the full tuple and returns one consistent journal snapshot.
func Lookup(ctx context.Context, journal JournalReader, sessionID, messageID string, sequence uint64) (messagejournal.Input, []messagejournal.Input, error) {
	if journal == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(messageID) == "" || sequence == 0 {
		return messagejournal.Input{}, nil, ErrInvalidReceipt
	}
	inputs, err := journal.Inputs(ctx, sessionID)
	if err != nil {
		return messagejournal.Input{}, nil, err
	}
	for _, input := range inputs {
		if input.SessionID == sessionID && input.MessageID == messageID && input.Sequence == sequence {
			return input, inputs, nil
		}
	}
	return messagejournal.Input{}, nil, ErrInvalidReceipt
}

// RootReady is for fresh-root admission only, not the live steering path.
// An earlier accepted input is already fenced from replay and must not block a
// later input after an archive reopen. Unknown or failed delivery remains a
// barrier because its provider hand-off was never durably classified.
func RootReady(ctx context.Context, journal JournalReader, sessionID, messageID string, sequence uint64) (bool, error) {
	input, inputs, err := Lookup(ctx, journal, sessionID, messageID, sequence)
	if err != nil {
		return false, err
	}
	if input.Phase != messagejournal.InputPending {
		return false, ErrInvalidReceipt
	}
	for _, prior := range inputs {
		if prior.Sequence < sequence && prior.Phase == messagejournal.InputAccepted {
			continue
		}
		if prior.Sequence < sequence && prior.Phase != messagejournal.InputCompleted && prior.Phase != messagejournal.InputTerminalFailed {
			return false, nil
		}
	}
	return true, nil
}

// VerifyAcceptance accepts a late observation without downgrading any terminal.
func VerifyAcceptance(ctx context.Context, journal JournalReader, sessionID, messageID string, sequence uint64) error {
	input, _, err := Lookup(ctx, journal, sessionID, messageID, sequence)
	if err == nil && input.Phase != messagejournal.InputAccepted && input.Phase != messagejournal.InputCompleted && input.Phase != messagejournal.InputTerminalFailed && input.Phase != messagejournal.InputFailed {
		err = ErrInvalidReceipt
	}
	return err
}

type JournalWriter interface {
	JournalReader
	MarkInputAccepted(context.Context, string, string, string) (messagejournal.Input, error)
}

// Commit retries only the exact acceptance write, never the provider request.
func Commit(ctx context.Context, journal JournalWriter, sessionID, messageID string, sequence uint64, owner string) error {
	return new(Fence).Commit(ctx, journal, sessionID, messageID, sequence, owner)
}

// Commit keeps an in-memory replay fence if this exact acceptance write fails.
func (f *Fence) Commit(ctx context.Context, journal JournalWriter, sessionID, messageID string, sequence uint64, owner string) error {
	input, _, err := Lookup(ctx, journal, sessionID, messageID, sequence)
	if err != nil {
		return err
	}
	if input.Phase == messagejournal.InputAccepted || input.Phase == messagejournal.InputCompleted || input.Phase == messagejournal.InputTerminalFailed || input.Phase == messagejournal.InputFailed {
		return nil
	}
	if input.Phase != messagejournal.InputPending || input.Lease.Owner != owner || owner == "" {
		return ErrInvalidReceipt
	}
	key := receipt{sessionID, messageID, sequence}
	f.Observe(sessionID, messageID, sequence)
	accepted, err := journal.MarkInputAccepted(context.WithoutCancel(ctx), sessionID, messageID, owner)
	if err != nil {
		accepted, err = journal.MarkInputAccepted(context.WithoutCancel(ctx), sessionID, messageID, owner)
	}
	if err != nil {
		return err
	}
	if accepted.SessionID != sessionID || accepted.MessageID != messageID || accepted.Sequence != sequence || accepted.Phase != messagejournal.InputAccepted {
		return ErrInvalidReceipt
	}
	f.clear(key)
	return nil
}
