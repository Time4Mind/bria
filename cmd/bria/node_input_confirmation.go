package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/promptidentity"
	"github.com/Time4Mind/bria/internal/providerbinding"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/transcript"
)

const inputConfirmationPollInterval = 75 * time.Millisecond
const inputConfirmationUserTail = 8

var errProviderBindingMissing = errors.New("provider binding is not available yet")

type providerBindingLookup interface {
	LookupRef(domain.SessionRef) (providerbinding.Record, bool, error)
}

type contextualProviderBindingLookup interface {
	LookupRefContext(context.Context, domain.SessionRef) (providerbinding.Record, bool, error)
}

type providerTranscriptInputConfirmer struct {
	bindings providerBindingLookup
	reader   inputTranscriptReader
	now      func() time.Time
}

type inputTranscriptReader interface {
	Read(context.Context, transcript.Request) ([]transcript.Event, error)
}

func newProviderTranscriptInputConfirmer(
	bindings any,
	reader inputTranscriptReader,
) (*providerTranscriptInputConfirmer, error) {
	lookup, ok := bindings.(providerBindingLookup)
	if !ok || reader == nil {
		return nil, errors.New("provider input confirmation dependencies are unavailable")
	}
	return &providerTranscriptInputConfirmer{bindings: lookup, reader: reader, now: time.Now}, nil
}

func (c *providerTranscriptInputConfirmer) BaselineInput(
	ctx context.Context,
	binding runtimehost.RuntimeBinding,
) (runtimehost.InputConfirmationBaseline, error) {
	record, err := c.currentBinding(ctx, binding, "")
	if errors.Is(err, errProviderBindingMissing) {
		return runtimehost.InputConfirmationBaseline{CapturedAt: c.now().UTC()}, nil
	}
	if err != nil {
		return runtimehost.InputConfirmationBaseline{}, err
	}
	baseline := runtimehost.InputConfirmationBaseline{
		ProviderSessionID: record.ProviderSessionID,
		CapturedAt:        c.now().UTC(),
		LegacyGeneration:  record.RuntimeGeneration == 0,
	}
	events, err := c.read(ctx, binding, record)
	if errors.Is(err, transcript.ErrTranscriptNotFound) {
		return baseline, nil
	}
	if err != nil {
		return runtimehost.InputConfirmationBaseline{}, err
	}
	for _, event := range events {
		if event.Kind == transcript.EventUserText {
			baseline.UserTail = append(baseline.UserTail, inputUserFingerprint(event))
		}
	}
	if len(baseline.UserTail) > inputConfirmationUserTail {
		baseline.UserTail = append([]string(nil),
			baseline.UserTail[len(baseline.UserTail)-inputConfirmationUserTail:]...)
	}
	return baseline, nil
}

func (c *providerTranscriptInputConfirmer) ConfirmInput(
	ctx context.Context,
	binding runtimehost.RuntimeBinding,
	baseline runtimehost.InputConfirmationBaseline,
	promptDigest string,
) error {
	if len(promptDigest) != promptidentity.DigestLength {
		return errors.New("provider input confirmation identity is invalid")
	}
	expectedProviderID := baseline.ProviderSessionID
	ticker := time.NewTicker(inputConfirmationPollInterval)
	defer ticker.Stop()
	for {
		record, err := c.currentBinding(ctx, binding, expectedProviderID)
		if err == nil {
			if expectedProviderID == "" {
				expectedProviderID = record.ProviderSessionID
			}
			events, readErr := c.read(ctx, binding, record)
			if readErr == nil && transcriptContainsNewPrompt(events, baseline, promptDigest) {
				return nil
			}
			if readErr != nil && !errors.Is(readErr, transcript.ErrTranscriptNotFound) &&
				!errors.Is(readErr, context.DeadlineExceeded) &&
				!errors.Is(readErr, context.Canceled) {
				// Transient file/index errors are retried inside the bounded waiter.
			}
		} else if errors.Is(err, runtimehost.ErrStaleRuntime) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *providerTranscriptInputConfirmer) currentBinding(
	ctx context.Context,
	binding runtimehost.RuntimeBinding,
	expectedProviderID string,
) (providerbinding.Record, error) {
	ref := domain.SessionRef{NodeID: domain.NodeID(binding.NodeID), SessionID: domain.SessionID(binding.SessionID)}
	var record providerbinding.Record
	var found bool
	var err error
	if lookup, ok := c.bindings.(contextualProviderBindingLookup); ok {
		record, found, err = lookup.LookupRefContext(ctx, ref)
	} else {
		if err = ctx.Err(); err == nil {
			record, found, err = c.bindings.LookupRef(ref)
		}
	}
	if err != nil {
		return providerbinding.Record{}, err
	}
	if !found {
		return providerbinding.Record{}, errProviderBindingMissing
	}
	if (record.RuntimeGeneration != 0 && record.RuntimeGeneration != binding.Generation) ||
		(expectedProviderID != "" && record.ProviderSessionID != expectedProviderID) {
		return providerbinding.Record{}, runtimehost.ErrStaleRuntime
	}
	return record, nil
}

func (c *providerTranscriptInputConfirmer) read(
	ctx context.Context,
	binding runtimehost.RuntimeBinding,
	record providerbinding.Record,
) ([]transcript.Event, error) {
	return c.reader.Read(ctx, transcript.Request{
		Backend:           transcript.Backend(binding.Backend),
		ProviderSessionID: record.ProviderSessionID,
		Workdir:           record.Workdir,
	})
}

func transcriptContainsNewPrompt(
	events []transcript.Event,
	baseline runtimehost.InputConfirmationBaseline,
	promptDigest string,
) bool {
	users := make([]transcript.Event, 0, len(events))
	fingerprints := make([]string, 0, len(events))
	for _, event := range events {
		if event.Kind == transcript.EventUserText {
			users = append(users, event)
			fingerprints = append(fingerprints, inputUserFingerprint(event))
		}
	}
	start := 0
	if len(baseline.UserTail) > 0 {
		if index := lastFingerprintSequence(fingerprints, baseline.UserTail); index >= 0 {
			start = index + len(baseline.UserTail)
		} else {
			// The bounded transcript window was rewritten or shifted past the
			// baseline. Without an exact append boundary, fail closed rather than
			// let an older identical prompt prove this submission.
			return false
		}
	}
	for _, event := range users[start:] {
		if len(baseline.UserTail) == 0 && !baseline.CapturedAt.IsZero() {
			at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
			if err != nil || !at.After(baseline.CapturedAt) {
				continue
			}
		}
		if promptidentity.Digest(strings.TrimSpace(event.Text)) == promptDigest {
			return true
		}
	}
	return false
}

func lastFingerprintSequence(values, tail []string) int {
	if len(tail) == 0 || len(tail) > len(values) {
		return -1
	}
	for start := len(values) - len(tail); start >= 0; start-- {
		matched := true
		for index := range tail {
			if values[start+index] != tail[index] {
				matched = false
				break
			}
		}
		if matched {
			return start
		}
	}
	return -1
}

func inputUserFingerprint(event transcript.Event) string {
	return promptidentity.Digest(strings.TrimSpace(event.Text) + "\x00" + event.Timestamp)
}
