package messagejournal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"
)

func (journal *Journal) EnqueueInput(
	ctx context.Context,
	sessionID string,
	messageID string,
	payload []byte,
) (Input, bool, error) {
	return journal.EnqueueInputWithAttachments(ctx, sessionID, messageID, payload, nil)
}

func (journal *Journal) EnqueueInputWithAttachments(
	ctx context.Context,
	sessionID string,
	messageID string,
	payload []byte,
	attachments []AttachmentRef,
) (Input, bool, error) {
	if err := ctx.Err(); err != nil {
		return Input{}, false, err
	}
	if err := journal.validateInputValues(sessionID, messageID, payload, attachments); err != nil {
		return Input{}, false, err
	}
	var result Input
	inserted := false
	err := journal.mutate(func(loaded *document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(loaded, sessionID, true, journal.limits)
		if err != nil {
			return err
		}
		for _, record := range session.Inputs {
			if record.MessageID != messageID {
				continue
			}
			if !bytes.Equal(record.Payload, payload) || !equalAttachments(record.Attachments, attachments) {
				return ErrConflict
			}
			result = inputFromRecord(sessionID, record)
			return errNoMutation
		}
		if len(session.Inputs) == journal.limits.MaxInputsPerSession {
			return ErrJournalFull
		}
		pending := 0
		for _, record := range session.Inputs {
			if record.Phase == InputPending {
				pending++
			}
		}
		if pending == journal.limits.MaxPendingInputsPerSession {
			return ErrQueueFull
		}
		if session.NextSequence == ^uint64(0) {
			return ErrJournalFull
		}
		session.NextSequence++
		record := inputRecord{
			MessageID:   messageID,
			Sequence:    session.NextSequence,
			Payload:     append([]byte(nil), payload...),
			Attachments: attachmentRecords(attachments),
			Phase:       InputPending,
		}
		session.Inputs = append(session.Inputs, record)
		result = inputFromRecord(sessionID, record)
		inserted = true
		return nil
	})
	if errors.Is(err, errNoMutation) {
		return result, false, nil
	}
	return result, inserted, err
}

func (journal *Journal) Inputs(ctx context.Context, sessionID string) ([]Input, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return nil, err
	}
	var result []Input
	err := journal.inspect(func(loaded document) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		session, err := sessionAt(&loaded, sessionID, false, journal.limits)
		if errors.Is(err, ErrNotFound) {
			result = []Input{}
			return nil
		}
		if err != nil {
			return err
		}
		result = make([]Input, len(session.Inputs))
		for index, record := range session.Inputs {
			result[index] = inputFromRecord(sessionID, record)
		}
		return nil
	})
	return result, err
}

func (journal *Journal) LeaseNextInput(
	ctx context.Context,
	sessionID string,
	owner string,
	now time.Time,
	duration time.Duration,
) (Input, error) {
	if err := journal.validateLeaseRequest(ctx, sessionID, owner, now, duration); err != nil {
		return Input{}, err
	}
	var result Input
	err := journal.mutate(func(loaded *document) error {
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil {
			return ErrNoAvailable
		}
		for index := range session.Inputs {
			record := &session.Inputs[index]
			switch record.Phase {
			case InputAccepted, InputCompleted:
				continue
			case InputFailed, InputUnknown:
				// A later message must not overtake an earlier failed
				// hand-off. RetryInput is the explicit unblock operation.
				return ErrNoAvailable
			case InputPending:
				// Continue below.
			default:
				return ErrInvalidFormat
			}
			if record.Lease.Owner != "" && now.UnixNano() < record.Lease.UntilUnix {
				return ErrNoAvailable
			}
			record.Lease = leaseRecord{Owner: owner, UntilUnix: now.Add(duration).UnixNano()}
			result = inputFromRecord(sessionID, *record)
			return nil
		}
		return ErrNoAvailable
	})
	return result, err
}

func (journal *Journal) MarkInputAccepted(ctx context.Context, sessionID, messageID, owner string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase == InputAccepted {
			return errNoMutation
		}
		if record.Phase != InputPending {
			return ErrInvalidTransition
		}
		if record.Lease.Owner != owner || owner == "" {
			return ErrLeaseOwner
		}
		record.Phase = InputAccepted
		record.Lease = leaseRecord{}
		return nil
	})
}

// ReleaseInputLease returns an unaccepted input to the head of its ordered
// lane. Dispatchers use it when Submit definitively reports that no provider
// hand-off began (for example, an executor is temporarily not ready).
func (journal *Journal) ReleaseInputLease(ctx context.Context, sessionID, messageID, owner string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase != InputPending {
			return ErrInvalidTransition
		}
		if record.Lease == (leaseRecord{}) {
			return errNoMutation
		}
		if record.Lease.Owner != owner || owner == "" {
			return ErrLeaseOwner
		}
		record.Lease = leaseRecord{}
		return nil
	})
}

// MarkInputDeliveryFailed records a definite failure before provider
// acceptance. It keeps the record in place and blocks later input until an
// explicit RetryInput, so a failed hand-off cannot silently reorder the lane.
func (journal *Journal) MarkInputDeliveryFailed(ctx context.Context, sessionID, messageID, owner string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase == InputFailed {
			return errNoMutation
		}
		if record.Phase != InputPending {
			return ErrInvalidTransition
		}
		if record.Lease.Owner != owner || owner == "" {
			return ErrLeaseOwner
		}
		record.Phase = InputFailed
		record.Lease = leaseRecord{}
		return nil
	})
}

// MarkInputDeliveryUnknown records an ambiguous provider hand-off from a
// leased pending input. The provider may already have accepted it, so the
// ordered lane remains blocked until an explicit RetryInput.
func (journal *Journal) MarkInputDeliveryUnknown(ctx context.Context, sessionID, messageID, owner string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase == InputUnknown {
			return errNoMutation
		}
		if record.Phase != InputPending {
			return ErrInvalidTransition
		}
		if record.Lease.Owner != owner || owner == "" {
			return ErrLeaseOwner
		}
		record.Phase = InputUnknown
		record.Lease = leaseRecord{}
		return nil
	})
}

func (journal *Journal) CompleteInput(ctx context.Context, sessionID, messageID string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase == InputCompleted {
			return errNoMutation
		}
		if record.Phase != InputAccepted {
			return ErrInvalidTransition
		}
		record.Phase = InputCompleted
		return nil
	})
}

// FailInput records a terminal provider failure after the provider previously
// acknowledged this input. Like a delivery failure, it requires an explicit
// RetryInput before later pending input can advance.
func (journal *Journal) FailInput(ctx context.Context, sessionID, messageID string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase == InputFailed {
			return errNoMutation
		}
		if record.Phase != InputAccepted {
			return ErrInvalidTransition
		}
		record.Phase = InputFailed
		return nil
	})
}

// MarkInputUnknown records that an already accepted provider turn cannot be
// reconciled with provider history after recovery. It is observable and
// blocks automatic replay until an explicit RetryInput.
func (journal *Journal) MarkInputUnknown(ctx context.Context, sessionID, messageID string) (Input, error) {
	return journal.transitionInput(ctx, sessionID, messageID, func(record *inputRecord) error {
		if record.Phase == InputUnknown {
			return errNoMutation
		}
		if record.Phase != InputAccepted {
			return ErrInvalidTransition
		}
		record.Phase = InputUnknown
		return nil
	})
}

func (journal *Journal) RetryInput(ctx context.Context, sessionID, messageID string) (Input, error) {
	if err := ctx.Err(); err != nil {
		return Input{}, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return Input{}, err
	}
	if err := validateOpaqueID(messageID, journal.limits.MaxIDBytes, "message id"); err != nil {
		return Input{}, err
	}
	var result Input
	err := journal.mutate(func(loaded *document) error {
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil {
			return ErrNotFound
		}
		pending := 0
		for _, record := range session.Inputs {
			if record.Phase == InputPending {
				pending++
			}
		}
		for index := range session.Inputs {
			record := &session.Inputs[index]
			if record.MessageID != messageID {
				continue
			}
			result = inputFromRecord(sessionID, *record)
			if record.Phase == InputPending {
				return errNoMutation
			}
			if record.Phase != InputFailed && record.Phase != InputUnknown {
				return ErrInvalidTransition
			}
			if pending == journal.limits.MaxPendingInputsPerSession {
				return ErrQueueFull
			}
			record.Phase = InputPending
			record.Lease = leaseRecord{}
			result = inputFromRecord(sessionID, *record)
			return nil
		}
		return ErrNotFound
	})
	if errors.Is(err, errNoMutation) {
		return result, nil
	}
	return result, err
}

var errNoMutation = errors.New("message journal mutation is already applied")

func (journal *Journal) transitionInput(
	ctx context.Context,
	sessionID string,
	messageID string,
	transition func(*inputRecord) error,
) (Input, error) {
	if err := ctx.Err(); err != nil {
		return Input{}, err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return Input{}, err
	}
	if err := validateOpaqueID(messageID, journal.limits.MaxIDBytes, "message id"); err != nil {
		return Input{}, err
	}
	var result Input
	err := journal.mutate(func(loaded *document) error {
		session, err := sessionAt(loaded, sessionID, false, journal.limits)
		if err != nil {
			return ErrNotFound
		}
		for index := range session.Inputs {
			record := &session.Inputs[index]
			if record.MessageID != messageID {
				continue
			}
			err := transition(record)
			result = inputFromRecord(sessionID, *record)
			return err
		}
		return ErrNotFound
	})
	if errors.Is(err, errNoMutation) {
		return result, nil
	}
	return result, err
}

func (journal *Journal) validateInputValues(sessionID, messageID string, payload []byte, attachments []AttachmentRef) error {
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return err
	}
	if err := validateOpaqueID(messageID, journal.limits.MaxIDBytes, "message id"); err != nil {
		return err
	}
	if len(payload) > journal.limits.MaxPayloadBytes {
		return fmt.Errorf("input payload exceeds %d bytes", journal.limits.MaxPayloadBytes)
	}
	for _, attachment := range attachmentRecords(attachments) {
		if validateAttachmentRecord(attachment, journal.limits) != nil {
			return errors.New("invalid input attachment")
		}
	}
	return nil
}

func (journal *Journal) validateLeaseRequest(
	ctx context.Context,
	sessionID string,
	owner string,
	now time.Time,
	duration time.Duration,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateOpaqueID(sessionID, journal.limits.MaxIDBytes, "session id"); err != nil {
		return err
	}
	if err := validateOpaqueID(owner, journal.limits.MaxIDBytes, "lease owner"); err != nil {
		return err
	}
	if now.IsZero() || duration <= 0 || duration > journal.limits.MaxLeaseDuration || now.Add(duration).UnixNano() <= 0 {
		return errors.New("lease time or duration is invalid")
	}
	return nil
}

func inputFromRecord(sessionID string, record inputRecord) Input {
	return Input{
		MessageID:   record.MessageID,
		SessionID:   sessionID,
		Sequence:    record.Sequence,
		Payload:     append([]byte(nil), record.Payload...),
		Attachments: attachmentRefs(record.Attachments),
		Phase:       record.Phase,
		Lease:       leaseFromRecord(record.Lease),
	}
}

func attachmentRecords(attachments []AttachmentRef) []attachmentRecord {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]attachmentRecord, len(attachments))
	for index, attachment := range attachments {
		result[index] = attachmentRecord{Reference: attachment.Reference, Size: attachment.Size, SHA256: attachment.SHA256}
	}
	return result
}

func attachmentRefs(attachments []attachmentRecord) []AttachmentRef {
	if len(attachments) == 0 {
		return nil
	}
	result := make([]AttachmentRef, len(attachments))
	for index, attachment := range attachments {
		result[index] = AttachmentRef{Reference: attachment.Reference, Size: attachment.Size, SHA256: attachment.SHA256}
	}
	return result
}

func equalAttachments(stored []attachmentRecord, supplied []AttachmentRef) bool {
	return reflect.DeepEqual(stored, attachmentRecords(supplied))
}

func leaseFromRecord(record leaseRecord) Lease {
	if record == (leaseRecord{}) {
		return Lease{}
	}
	return Lease{Owner: record.Owner, Until: time.Unix(0, record.UntilUnix).UTC()}
}
