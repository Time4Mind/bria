package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	voiceBaselineTimeout = 250 * time.Millisecond
	voicePendingLifetime = 10 * time.Minute
)

type voicePendingKey struct {
	userID domain.UserID
	ref    domain.SessionRef
}

type voicePendingBaseline struct {
	ref           domain.SessionRef
	events        []transcript.Event
	lastUserEvent string
	receivedAt    time.Time
	known         bool
}

type voicePending struct {
	operationID   string
	baselineEvent string
	ordinal       int
	acceptedAt    time.Time
	baselineKnown bool
}

func (h *Handler) captureVoiceBaseline(
	ctx context.Context,
	actor application.Principal,
) voicePendingBaseline {
	receivedAt := time.Now()
	session, err := h.service.ActiveSession(actor)
	if err != nil || h.controls == nil {
		return voicePendingBaseline{receivedAt: receivedAt}
	}
	baseline := voicePendingBaseline{ref: session.Ref(), receivedAt: receivedAt}
	baselineCtx, cancel := context.WithTimeout(ctx, voiceBaselineTimeout)
	defer cancel()
	events, err := h.controls.Transcript(baselineCtx, actor, session.Ref())
	if err != nil {
		return baseline
	}
	baseline.events = events
	baseline.lastUserEvent = lastTranscriptUserEvent(events)
	baseline.known = true
	return baseline
}

func (h *Handler) markVoicePending(
	actor application.Principal,
	ref domain.SessionRef,
	operationID string,
	baseline voicePendingBaseline,
) {
	key := voicePendingKey{userID: actor.UserID, ref: ref}
	pending := voicePending{
		operationID: operationID, acceptedAt: baseline.receivedAt,
		baselineKnown: baseline.known && baseline.ref == ref,
		ordinal:       1,
	}
	if pending.acceptedAt.IsZero() {
		pending.acceptedAt = time.Now()
	}
	if pending.baselineKnown {
		pending.baselineEvent = baseline.lastUserEvent
	}
	h.voiceMu.Lock()
	queue := h.pendingVoices[key]
	if pending.baselineKnown && len(queue) > 0 &&
		queue[len(queue)-1].baselineKnown &&
		queue[len(queue)-1].baselineEvent == pending.baselineEvent {
		pending.ordinal = queue[len(queue)-1].ordinal + 1
	}
	h.pendingVoices[key] = append(queue, pending)
	h.voiceMu.Unlock()
}

func (h *Handler) withPendingVoiceRows(
	actor application.Principal,
	ref domain.SessionRef,
	session domain.Session,
	events []transcript.Event,
) []transcript.Event {
	key := voicePendingKey{userID: actor.UserID, ref: ref}
	now := time.Now()

	h.voiceMu.Lock()
	queue := h.pendingVoices[key]
	remaining := queue[:0]
	for _, pending := range queue {
		resolved := false
		if pending.baselineKnown {
			after, found := transcriptUserEventsAfter(events, pending.baselineEvent)
			resolved = after >= pending.ordinal
			if !found {
				resolved = transcriptUserEventsSince(events, pending.acceptedAt) >= pending.ordinal
			}
		}
		if !resolved && !pending.baselineKnown {
			resolved = transcriptUserEventsSince(events, pending.acceptedAt) >= pending.ordinal
		}
		failed := session.LastOperation != nil &&
			session.LastOperation.OperationID == pending.operationID &&
			session.LastOperation.Status == domain.OperationFailed
		if resolved || failed || now.Sub(pending.acceptedAt) >= voicePendingLifetime {
			continue
		}
		remaining = append(remaining, pending)
	}
	if len(remaining) == 0 {
		delete(h.pendingVoices, key)
	} else {
		h.pendingVoices[key] = remaining
	}
	pendingCount := len(remaining)
	h.voiceMu.Unlock()

	if pendingCount == 0 {
		return events
	}
	result := append([]transcript.Event(nil), events...)
	for index := 0; index < pendingCount; index++ {
		result = append(result, transcript.Event{
			Kind: transcript.EventUserText,
			Text: h.copy(actor).Text(i18n.VoiceTranscribing),
		})
	}
	return result
}

func (h *Handler) pendingVoiceCard(
	actor application.Principal,
	ref domain.SessionRef,
	baseline voicePendingBaseline,
) (telegramui.Screen, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if baseline.known && baseline.ref == ref {
		events := h.withPendingVoiceRows(actor, ref, session, baseline.events)
		return h.projector.SessionCardPage(actor, ref, cardEvents(events), 0)
	}
	// A brand-new provider may not have created its transcript yet. Still feed
	// the pending voice row through the normal card event renderer so it stays
	// with the active session content and before the background-session panel.
	events := h.withPendingVoiceRows(actor, ref, session, nil)
	return h.projector.SessionCardPage(actor, ref, cardEvents(events), 0)
}

func lastTranscriptUserEvent(events []transcript.Event) string {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind == transcript.EventUserText {
			return transcriptEventKey(events[index])
		}
	}
	return ""
}

func transcriptUserEventsAfter(events []transcript.Event, baseline string) (int, bool) {
	if baseline == "" {
		count := 0
		for _, event := range events {
			if event.Kind == transcript.EventUserText {
				count++
			}
		}
		return count, true
	}
	found := false
	count := 0
	for _, event := range events {
		if event.Kind != transcript.EventUserText {
			continue
		}
		if found {
			count++
			continue
		}
		found = transcriptEventKey(event) == baseline
	}
	if !found {
		return 0, false
	}
	return count, true
}

func transcriptUserEventsSince(events []transcript.Event, since time.Time) int {
	count := 0
	for _, event := range events {
		if event.Kind != transcript.EventUserText {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err == nil && !at.Before(since) {
			count++
		}
	}
	return count
}

func transcriptEventKey(event transcript.Event) string {
	return event.Timestamp + "\x00" + event.Text
}
