package messagejournal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (journal *Journal) EnqueueOutput(
	ctx context.Context,
	sessionID string,
	operationID string,
	kind string,
	payload []byte,
) (Output, bool, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, false, err
	}
	if err := journal.validateOutputValues(sessionID, operationID, kind, payload); err != nil {
		return Output{}, false, err
	}
	var result Output
	inserted := false
	err := journal.mutate(func(loaded *document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(loaded, sessionID, true, journal.limits)
		if err != nil {
			return err
		}
		for _, record := range session.Outputs {
			if record.OperationID != operationID {
				continue
			}
			if record.Kind != kind || !bytes.Equal(record.Payload, payload) {
				return ErrConflict
			}
			result = outputFromRecord(sessionID, record)
			return errNoMutation
		}
		if len(session.Outputs) == journal.limits.MaxOutputsPerSession || session.NextSequence == ^uint64(0) {
			return ErrJournalFull
		}
		session.NextSequence++
		record := outputRecord{
			OperationID: operationID,
			Sequence:    session.NextSequence,
			Kind:        kind,
			Payload:     append([]byte(nil), payload...),
			Phase:       OutputPending,
		}
		session.Outputs = append(session.Outputs, record)
		result = outputFromRecord(sessionID, record)
		inserted = true
		return nil
	})
	if errors.Is(err, errNoMutation) {
		return result, false, nil
	}
	return result, inserted, err
}

func (journal *Journal) Outputs(ctx context.Context, sessionID string) ([]Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return nil, err
	}
	var result []Output
	err := journal.inspect(func(loaded document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(&loaded, sessionID, false, journal.limits)
		if errors.Is(err, ErrNotFound) {
			result = []Output{}
			return nil
		}
		if err != nil {
			return err
		}
		result = make([]Output, len(session.Outputs))
		for index, record := range session.Outputs {
			result[index] = outputFromRecord(sessionID, record)
		}
		return nil
	})
	return result, err
}

func (journal *Journal) LeaseNextOutput(
	ctx context.Context,
	sessionID string,
	owner string,
	now time.Time,
	duration time.Duration,
) (Output, error) {
	return journal.LeaseNextOutputForSessions(ctx, []string{sessionID}, owner, now, duration)
}

// LeaseNextOutputForSessions scans one journal snapshot in caller-provided
// session order. It avoids decoding the complete journal once per idle session.
func (journal *Journal) LeaseNextOutputForSessions(
	ctx context.Context,
	sessionIDs []string,
	owner string,
	now time.Time,
	duration time.Duration,
) (Output, error) {
	if len(sessionIDs) == 0 {
		return Output{}, ErrNoAvailable
	}
	if err := journal.validateLeaseRequest(ctx, sessionIDs[0], owner, now, duration); err != nil {
		return Output{}, err
	}
	for _, sessionID := range sessionIDs[1:] {
		if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
			return Output{}, err
		}
	}
	var result Output
	err := journal.mutate(func(loaded *document) error {
		for _, sessionID := range sessionIDs {
			session, sessionErr := sessionAt(loaded, sessionID, false, journal.limits)
			if errors.Is(sessionErr, ErrNotFound) {
				continue
			}
			if sessionErr != nil {
				return sessionErr
			}
			leased, available, leaseErr := leaseNextOutput(sessionID, session, owner, now, duration)
			if leaseErr != nil {
				return leaseErr
			}
			if available {
				result = leased
				return nil
			}
		}
		return ErrNoAvailable
	})
	return result, err
}

func leaseNextOutput(sessionID string, session *sessionRecord, owner string, now time.Time, duration time.Duration) (Output, bool, error) {
	for index := range session.Outputs {
		record := &session.Outputs[index]
		switch record.Phase {
		case OutputConfirmed:
			continue
		case OutputFailed, OutputUnknown, OutputSuperseded:
			// Terminal unconfirmed writes are never replayed implicitly,
			// but they do not block independent later state/results.
			continue
		case OutputPending:
			if record.Lease.Owner != "" && now.UnixNano() < record.Lease.UntilUnix {
				return Output{}, false, nil
			}
			record.Lease = leaseRecord{Owner: owner, UntilUnix: now.Add(duration).UnixNano()}
			return outputFromRecord(sessionID, *record), true, nil
		default:
			return Output{}, false, ErrInvalidFormat
		}
	}
	return Output{}, false, nil
}

// SupersedePendingOutputs retains durable history while collapsing unleased
// intermediate projections to the newest complete state. A leased write is
// never changed because its external outcome may already be in flight.
func (journal *Journal) SupersedePendingOutputs(ctx context.Context, sessionID, keepOperationID string, kinds []string) ([]Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return nil, err
	}
	if err := validateOpaqueID(keepOperationID, journal.limits.MaxIDBytes, "operation id"); err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		if strings.TrimSpace(kind) == "" || kind != strings.TrimSpace(kind) || len(kind) > journal.limits.MaxKindBytes {
			return nil, errors.New("output kind is invalid")
		}
		wanted[kind] = true
	}
	if len(wanted) == 0 {
		return []Output{}, nil
	}
	var result []Output
	err := journal.mutate(func(loaded *document) error {
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil {
			return err
		}
		var keepSequence uint64
		for index := range session.Outputs {
			if session.Outputs[index].OperationID == keepOperationID {
				keepSequence = session.Outputs[index].Sequence
				break
			}
		}
		if keepSequence == 0 {
			return ErrNotFound
		}
		for index := range session.Outputs {
			record := &session.Outputs[index]
			if record.OperationID == keepOperationID {
				continue
			}
			if record.Sequence >= keepSequence || record.Phase != OutputPending || record.Lease.Owner != "" || !wanted[record.Kind] {
				continue
			}
			record.Phase = OutputSuperseded
			result = append(result, outputFromRecord(sessionID, *record))
		}
		if len(result) == 0 {
			return errNoMutation
		}
		return nil
	})
	if errors.Is(err, errNoMutation) {
		return result, nil
	}
	return result, err
}

func (journal *Journal) ConfirmOutput(ctx context.Context, sessionID, operationID, owner, receipt string) (Output, error) {
	if strings.TrimSpace(receipt) == "" || len(receipt) > journal.limits.MaxReceiptBytes {
		return Output{}, errors.New("output receipt is invalid")
	}
	return journal.transitionOutput(ctx, sessionID, operationID, func(record *outputRecord) error {
		if record.Phase == OutputConfirmed {
			if record.Receipt != receipt {
				return ErrConflict
			}
			return errNoMutation
		}
		if record.Phase != OutputPending {
			return ErrInvalidTransition
		}
		if record.Lease.Owner != owner || owner == "" {
			return ErrLeaseOwner
		}
		record.Phase = OutputConfirmed
		record.Receipt = receipt
		record.Lease = leaseRecord{}
		return nil
	})
}

func (journal *Journal) MarkOutputFailed(ctx context.Context, sessionID, operationID, owner string) (Output, error) {
	return journal.finishOutput(ctx, sessionID, operationID, owner, OutputFailed)
}

func (journal *Journal) MarkOutputUnknown(ctx context.Context, sessionID, operationID, owner string) (Output, error) {
	return journal.finishOutput(ctx, sessionID, operationID, owner, OutputUnknown)
}

func (journal *Journal) finishOutput(
	ctx context.Context,
	sessionID string,
	operationID string,
	owner string,
	phase OutputPhase,
) (Output, error) {
	return journal.transitionOutput(ctx, sessionID, operationID, func(record *outputRecord) error {
		if record.Phase == phase {
			return errNoMutation
		}
		if record.Phase != OutputPending {
			return ErrInvalidTransition
		}
		if record.Lease.Owner != owner || owner == "" {
			return ErrLeaseOwner
		}
		record.Phase = phase
		record.Lease = leaseRecord{}
		return nil
	})
}

// RetryOutput is the explicit retry boundary. Unknown output is deliberately
// eligible only here; LeaseNextOutput never changes or leases it automatically.
func (journal *Journal) RetryOutput(ctx context.Context, sessionID, operationID string) (Output, error) {
	return journal.transitionOutput(ctx, sessionID, operationID, func(record *outputRecord) error {
		if record.Phase == OutputPending {
			return errNoMutation
		}
		if record.Phase != OutputFailed && record.Phase != OutputUnknown {
			return ErrInvalidTransition
		}
		record.Phase = OutputPending
		record.Lease = leaseRecord{}
		return nil
	})
}

func (journal *Journal) transitionOutput(
	ctx context.Context,
	sessionID string,
	operationID string,
	transition func(*outputRecord) error,
) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return Output{}, err
	}
	if err := validateOpaqueID(operationID, journal.limits.MaxIDBytes, "operation id"); err != nil {
		return Output{}, err
	}
	var result Output
	err := journal.mutate(func(loaded *document) error {
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil {
			return ErrNotFound
		}
		for index := range session.Outputs {
			record := &session.Outputs[index]
			if record.OperationID != operationID {
				continue
			}
			err := transition(record)
			result = outputFromRecord(sessionID, *record)
			return err
		}
		return ErrNotFound
	})
	if errors.Is(err, errNoMutation) {
		return result, nil
	}
	return result, err
}

func (journal *Journal) validateOutputValues(sessionID, operationID, kind string, payload []byte) error {
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return err
	}
	if err := validateOpaqueID(operationID, journal.limits.MaxIDBytes, "operation id"); err != nil {
		return err
	}
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(kind) != kind || len(kind) > journal.limits.MaxKindBytes {
		return errors.New("output kind is invalid")
	}
	if len(payload) > journal.limits.MaxPayloadBytes {
		return fmt.Errorf("output payload exceeds %d bytes", journal.limits.MaxPayloadBytes)
	}
	return nil
}

func outputFromRecord(sessionID string, record outputRecord) Output {
	return Output{
		OperationID: record.OperationID,
		SessionID:   sessionID,
		Sequence:    record.Sequence,
		Kind:        record.Kind,
		Payload:     append([]byte(nil), record.Payload...),
		Phase:       record.Phase,
		Receipt:     record.Receipt,
		Lease:       leaseFromRecord(record.Lease),
	}
}
