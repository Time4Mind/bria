package telegramapp

import (
	"context"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	inputBaselineTimeout = 500 * time.Millisecond
	inputPendingLifetime = 10 * time.Minute
)

type inputPendingKey struct {
	userID domain.UserID
	ref    domain.SessionRef
}

type inputPendingBaseline struct {
	ref           domain.SessionRef
	events        []transcript.Event
	lastUserEvent string
	receivedAt    time.Time
	known         bool
}

type inputPending struct {
	operationID   string
	text          string
	baselineEvent string
	ordinal       int
	acceptedAt    time.Time
	baselineKnown bool
}

func pendingInputText(update telegrambot.IncomingUpdate) string {
	if update.Content.Kind == telegrambot.IncomingVoice {
		return ""
	}
	return strings.TrimSpace(update.Text)
}

// captureInputBaseline prefers the in-memory transcript that produced the
// currently visible card. It therefore preserves exactly what the user saw
// without a node round trip. A bounded transcript read covers process restarts
// where Telegram still has a card but the local rendering cache is cold.
func (h *Handler) captureInputBaseline(
	ctx context.Context,
	actor application.Principal,
) inputPendingBaseline {
	receivedAt := time.Now()
	session, err := h.service.ActiveSession(actor)
	if err != nil || h.controls == nil {
		return inputPendingBaseline{receivedAt: receivedAt}
	}
	baseline := inputPendingBaseline{ref: session.Ref(), receivedAt: receivedAt}
	if events, ok := h.cachedCardTranscript(session.Ref()); ok {
		baseline.events = events
		baseline.lastUserEvent = lastTranscriptUserEvent(events)
		baseline.known = true
		return baseline
	}
	baselineCtx, cancel := context.WithTimeout(ctx, inputBaselineTimeout)
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

func (h *Handler) markInputPending(
	actor application.Principal,
	ref domain.SessionRef,
	operationID string,
	text string,
	baseline inputPendingBaseline,
) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	key := inputPendingKey{userID: actor.UserID, ref: ref}
	pending := inputPending{
		operationID: operationID, text: text, acceptedAt: baseline.receivedAt,
		baselineKnown: baseline.known && baseline.ref == ref, ordinal: 1,
	}
	if pending.acceptedAt.IsZero() {
		pending.acceptedAt = time.Now()
	}
	if pending.baselineKnown {
		pending.baselineEvent = baseline.lastUserEvent
	}
	h.inputMu.Lock()
	queue := h.pendingInputs[key]
	if pending.baselineKnown && len(queue) > 0 &&
		queue[len(queue)-1].baselineKnown &&
		queue[len(queue)-1].baselineEvent == pending.baselineEvent {
		pending.ordinal = queue[len(queue)-1].ordinal + 1
	}
	h.pendingInputs[key] = append(queue, pending)
	h.inputMu.Unlock()
}

func (h *Handler) withPendingInputRows(
	actor application.Principal,
	ref domain.SessionRef,
	session domain.Session,
	events []transcript.Event,
) []transcript.Event {
	key := inputPendingKey{userID: actor.UserID, ref: ref}
	now := time.Now()

	h.inputMu.Lock()
	queue := h.pendingInputs[key]
	remaining := queue[:0]
	for _, pending := range queue {
		resolved := false
		if pending.baselineKnown {
			after, found := transcriptUserEventsAfter(events, pending.baselineEvent)
			resolved = found && after >= pending.ordinal
		} else {
			resolved = transcriptUserEventsSince(events, pending.acceptedAt) >= pending.ordinal
		}
		failed := session.LastOperation != nil &&
			session.LastOperation.OperationID == pending.operationID &&
			session.LastOperation.Status == domain.OperationFailed
		if resolved || failed || now.Sub(pending.acceptedAt) >= inputPendingLifetime {
			continue
		}
		remaining = append(remaining, pending)
	}
	if len(remaining) == 0 {
		delete(h.pendingInputs, key)
	} else {
		h.pendingInputs[key] = remaining
	}
	pendingRows := append([]inputPending(nil), remaining...)
	h.inputMu.Unlock()

	if len(pendingRows) == 0 {
		return events
	}
	result := append([]transcript.Event(nil), events...)
	for _, pending := range pendingRows {
		result = append(result, transcript.Event{
			Kind: transcript.EventUserText, Text: pending.text,
			Timestamp: pending.acceptedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return result
}

func (h *Handler) pendingInputCard(
	actor application.Principal,
	ref domain.SessionRef,
	baseline inputPendingBaseline,
) (telegramui.Screen, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil {
		return telegramui.Screen{}, err
	}
	events := []transcript.Event(nil)
	if baseline.known && baseline.ref == ref {
		events = baseline.events
	} else if cached, ok := h.cachedCardTranscript(ref); ok {
		events = cached
	}
	events = h.withPendingInputRows(actor, ref, session, events)
	return h.projector.SessionCardPageWithContext(
		actor, ref, cardEvents(events), 0, h.cardContext(ref),
	)
}
