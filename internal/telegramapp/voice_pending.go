package telegramapp

import (
	"context"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	voiceBaselineTimeout  = 250 * time.Millisecond
	voicePendingLifetime  = 10 * time.Minute
	maxVoiceConfirmations = 1024
)

type voicePendingKey struct {
	userID domain.UserID
	ref    domain.SessionRef
}

type voicePendingBaseline struct {
	ref            domain.SessionRef
	events         []transcript.Event
	lastUserEvent  string
	userEventCount int
	ordinal        int
	receivedAt     time.Time
	known          bool
}

type voicePending struct {
	operationID   string
	baselineEvent string
	baselineCount int
	ordinal       int
	acceptedAt    time.Time
	baselineKnown bool
	status        domain.OperationStatus
}

type voiceConfirmation struct {
	key         voicePendingKey
	operationID string
	at          time.Time
}

func (h *Handler) captureVoiceBaseline(
	ctx context.Context,
	actor application.Principal,
) voicePendingBaseline {
	receivedAt := time.Now()
	session, err := h.service.ActiveSession(actor)
	if err != nil || h.controls.transcript == nil {
		return voicePendingBaseline{receivedAt: receivedAt}
	}
	baseline := voicePendingBaseline{ref: session.Ref(), receivedAt: receivedAt}
	baselineCtx, cancel := context.WithTimeout(ctx, voiceBaselineTimeout)
	defer cancel()
	events, err := h.controls.transcript.Transcript(baselineCtx, actor, session.Ref())
	if err != nil {
		return baseline
	}
	baseline.events = events
	baseline.lastUserEvent = lastTranscriptUserEvent(events)
	baseline.userEventCount = transcriptUserEventCount(events)
	baseline.known = true
	return baseline
}

func (h *Handler) prepareVoiceBaseline(actor application.Principal, baseline *voicePendingBaseline) {
	baseline.ordinal = 1
	if !baseline.known || baseline.ref.SessionID == "" {
		return
	}
	key := voicePendingKey{userID: actor.UserID, ref: baseline.ref}
	h.voiceMu.Lock()
	defer h.voiceMu.Unlock()
	for _, pending := range h.pendingVoices[key] {
		if pending.baselineKnown && pending.baselineCount == baseline.userEventCount &&
			pending.ordinal >= baseline.ordinal {
			baseline.ordinal = pending.ordinal + 1
		}
	}
	if session, err := h.service.Session(actor, baseline.ref); err == nil {
		for _, acknowledgement := range session.VoiceAcknowledgements {
			if acknowledgement.BaselineKnown &&
				acknowledgement.BaselineCount == baseline.userEventCount &&
				acknowledgement.Ordinal >= baseline.ordinal {
				baseline.ordinal = acknowledgement.Ordinal + 1
			}
		}
	}
	if baseline.ordinal > 16 {
		baseline.ordinal = 16
	}
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
		baselineCount: baseline.userEventCount,
		ordinal:       baseline.ordinal, status: domain.OperationQueued,
	}
	if pending.acceptedAt.IsZero() {
		pending.acceptedAt = time.Now()
	}
	if pending.baselineKnown {
		pending.baselineEvent = baseline.lastUserEvent
	}
	h.voiceMu.Lock()
	queue := h.pendingVoices[key]
	if pending.ordinal == 0 {
		pending.ordinal = 1
	}
	h.pendingVoices[key] = append(queue, pending)
	h.voiceMu.Unlock()
}

func (h *Handler) hasPendingVoice(actor application.Principal, ref domain.SessionRef) bool {
	key := voicePendingKey{userID: actor.UserID, ref: ref}
	h.voiceMu.Lock()
	pending := len(h.pendingVoices[key]) > 0
	h.voiceMu.Unlock()
	return pending
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
	h.sweepVoiceConfirmationsLocked(now)
	queue := h.pendingVoices[key]
	known := make(map[string]bool, len(queue))
	for _, pending := range queue {
		known[pending.operationID] = true
	}
	fallbackAt := time.Time{}
	for _, acknowledgement := range session.VoiceAcknowledgements {
		if now.Sub(acknowledgement.AcceptedAt) >= voicePendingLifetime ||
			known[acknowledgement.OperationID] ||
			h.voiceConfirmedLocked(key, acknowledgement.OperationID) {
			continue
		}
		if fallbackAt.IsZero() || acknowledgement.AcceptedAt.Before(fallbackAt) {
			fallbackAt = acknowledgement.AcceptedAt
		}
		queue = append(queue, voicePending{
			operationID: acknowledgement.OperationID, acceptedAt: acknowledgement.AcceptedAt,
			status: acknowledgement.Status, baselineCount: acknowledgement.BaselineCount,
			baselineKnown: acknowledgement.BaselineKnown, ordinal: acknowledgement.Ordinal,
		})
	}
	for index := range queue {
		for _, acknowledgement := range session.VoiceAcknowledgements {
			if acknowledgement.OperationID == queue[index].operationID {
				queue[index].status = acknowledgement.Status
				break
			}
		}
	}
	remaining := queue[:0]
	confirmedOperations := make([]string, 0)
	for _, pending := range queue {
		resolved := false
		if pending.baselineKnown {
			after := transcriptUserEventCount(events) - pending.baselineCount
			found := after >= 0
			if pending.baselineEvent != "" {
				after, found = transcriptUserEventsAfter(events, pending.baselineEvent)
			}
			resolved = after >= pending.ordinal
			if !found {
				resolved = transcriptUserEventsSince(events, pending.acceptedAt) >= pending.ordinal
			}
		}
		if !resolved && !pending.baselineKnown {
			resolved = transcriptUserEventsSince(events, pending.acceptedAt) >= pending.ordinal
		}
		if !pending.baselineKnown && !fallbackAt.IsZero() {
			pending.ordinal = len(remaining) + 1
			resolved = transcriptUserEventsSince(events, fallbackAt) >= pending.ordinal
		}
		if resolved || now.Sub(pending.acceptedAt) >= voicePendingLifetime {
			if resolved && h.rememberVoiceConfirmationLocked(key, pending.operationID, now) {
				confirmedOperations = append(confirmedOperations, pending.operationID)
			}
			continue
		}
		remaining = append(remaining, pending)
	}
	if len(remaining) == 0 {
		delete(h.pendingVoices, key)
	} else {
		h.pendingVoices[key] = remaining
	}
	pendingRows := append([]voicePending(nil), remaining...)
	h.voiceMu.Unlock()
	for _, operationID := range confirmedOperations {
		processlog.Detailf(
			"bria voice_input: stage=transcript ref=%q operation=%q outcome=confirmed",
			ref.Key(), operationID,
		)
	}

	if len(pendingRows) == 0 {
		return events
	}
	result := append([]transcript.Event(nil), events...)
	for _, pending := range pendingRows {
		label := i18n.VoiceTranscribing
		switch pending.status {
		case domain.OperationSucceeded:
			label = i18n.VoiceRecognizedSent
		case domain.OperationFailed:
			label = i18n.VoiceDeliveryFailed
		}
		result = append(result, transcript.Event{
			Kind: transcript.EventUserText,
			Text: h.copy(actor).Text(label),
		})
	}
	return result
}

func (s *voicePendingState) voiceConfirmedLocked(key voicePendingKey, operationID string) bool {
	return s.confirmedVoices[key] != nil && !s.confirmedVoices[key][operationID].IsZero()
}

func (s *voicePendingState) rememberVoiceConfirmationLocked(
	key voicePendingKey,
	operationID string,
	at time.Time,
) bool {
	if operationID == "" || s.voiceConfirmedLocked(key, operationID) {
		return false
	}
	if s.confirmedVoices[key] == nil {
		s.confirmedVoices[key] = make(map[string]time.Time)
	}
	s.confirmedVoices[key][operationID] = at
	s.confirmedVoiceOrder = append(s.confirmedVoiceOrder, voiceConfirmation{
		key: key, operationID: operationID, at: at,
	})
	s.sweepVoiceConfirmationsLocked(at)
	return true
}

func (s *voicePendingState) sweepVoiceConfirmationsLocked(now time.Time) {
	remove := 0
	for remove < len(s.confirmedVoiceOrder) {
		entry := s.confirmedVoiceOrder[remove]
		if len(s.confirmedVoiceOrder)-remove <= maxVoiceConfirmations &&
			now.Sub(entry.at) < voicePendingLifetime {
			break
		}
		if current := s.confirmedVoices[entry.key]; current != nil &&
			current[entry.operationID] == entry.at {
			delete(current, entry.operationID)
			if len(current) == 0 {
				delete(s.confirmedVoices, entry.key)
			}
		}
		remove++
	}
	if remove > 0 {
		s.confirmedVoiceOrder = append([]voiceConfirmation(nil), s.confirmedVoiceOrder[remove:]...)
	}
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
	page := h.rememberedCardPage(actor.UserID, ref)
	anchor := h.rememberedCardAnchor(actor.UserID, ref, page)
	if baseline.known && baseline.ref == ref {
		events := h.withPendingVoiceRows(actor, ref, session, baseline.events)
		return h.projector.SessionCardViewWithContext(
			actor, ref, cardEvents(events), page, anchor, h.cardContext(ref),
		)
	}
	// A brand-new provider may not have created its transcript yet. Still feed
	// the pending voice row through the normal card event renderer so it stays
	// with the active session content and before the background-session panel.
	events := h.withPendingVoiceRows(actor, ref, session, nil)
	return h.projector.SessionCardViewWithContext(
		actor, ref, cardEvents(events), page, anchor, h.cardContext(ref),
	)
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

func transcriptUserEventCount(events []transcript.Event) int {
	count := 0
	for _, event := range events {
		if event.Kind == transcript.EventUserText {
			count++
		}
	}
	return count
}
