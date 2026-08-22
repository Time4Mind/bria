package telegramapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	maxPendingVoices      = 16
	maxVoiceConfirmations = 1024
)

type voicePendingKey struct {
	userID     domain.UserID
	ref        domain.SessionRef
	generation uint64
}

type voicePendingBaseline struct {
	ref            domain.SessionRef
	generation     uint64
	events         []transcript.Event
	lastUserEvent  string
	baselineID     string
	userEventCount int
	ordinal        int
	receivedAt     time.Time
	known          bool
}

type voicePending struct {
	operationID   string
	baselineEvent string
	baselineID    string
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
	baseline := voicePendingBaseline{
		ref: session.Ref(), generation: session.RuntimeGeneration, receivedAt: receivedAt,
	}
	baselineCtx, cancel := context.WithTimeout(ctx, voiceBaselineTimeout)
	defer cancel()
	events, err := h.controls.transcript.Transcript(baselineCtx, actor, session.Ref())
	if err != nil {
		return baseline
	}
	baseline.events = events
	baseline.lastUserEvent = lastTranscriptUserEvent(events)
	baseline.baselineID = transcriptBaselineID(baseline.lastUserEvent)
	baseline.userEventCount = transcriptUserEventCount(events)
	baseline.known = true
	return baseline
}

func (h *Handler) prepareVoiceBaseline(
	actor application.Principal,
	operationID string,
	baseline *voicePendingBaseline,
) (bool, bool) {
	baseline.ordinal = 1
	if baseline.ref.SessionID == "" {
		return true, false
	}
	session, sessionErr := h.service.Session(actor, baseline.ref)
	if sessionErr == nil {
		switch {
		case baseline.generation == 0:
			baseline.generation = session.RuntimeGeneration
		case baseline.generation != session.RuntimeGeneration:
			// Clear/restore can replace the provider runtime between the transcript
			// snapshot and admission. Discard that stale snapshot and track the
			// one submitted prompt from the new generation by its acceptance time.
			resetVoiceBaselineGeneration(baseline, session.RuntimeGeneration, time.Now())
		}
	}
	if sessionErr == nil {
		for _, acknowledgement := range session.VoiceAcknowledgements {
			if acknowledgement.OperationID != operationID {
				continue
			}
			baseline.known = acknowledgement.BaselineKnown
			baseline.userEventCount = acknowledgement.BaselineCount
			baseline.ordinal = acknowledgement.Ordinal
			return true, false
		}
	}
	key := voicePendingKey{
		userID: actor.UserID, ref: baseline.ref, generation: baseline.generation,
	}
	h.voiceMu.Lock()
	for _, pending := range h.pendingVoices[key] {
		if pending.operationID != operationID {
			continue
		}
		applyPendingVoiceBaseline(baseline, pending)
		h.voiceMu.Unlock()
		return true, false
	}
	h.voiceMu.Unlock()
	if sessionErr == nil && baseline.known {
		// Reconcile transcript-confirmed markers before admitting another voice,
		// otherwise a full but already-drained queue would reject new input.
		h.withPendingVoiceRows(actor, baseline.ref, session, baseline.events)
	}
	h.voiceMu.Lock()
	defer h.voiceMu.Unlock()
	for _, pending := range h.pendingVoices[key] {
		if pending.operationID == operationID {
			applyPendingVoiceBaseline(baseline, pending)
			return true, false
		}
	}
	unresolved := make(map[string]struct{}, maxPendingVoices)
	for _, pending := range h.pendingVoices[key] {
		unresolved[pending.operationID] = struct{}{}
		if !baseline.known {
			continue
		}
		if voiceBaselineMatches(
			pending.baselineKnown, pending.baselineCount, pending.baselineID, *baseline,
		) &&
			pending.ordinal >= baseline.ordinal {
			baseline.ordinal = pending.ordinal + 1
		}
	}
	if sessionErr == nil {
		for _, acknowledgement := range session.VoiceAcknowledgements {
			if h.voiceConfirmedLocked(key, acknowledgement.OperationID) {
				continue
			}
			unresolved[acknowledgement.OperationID] = struct{}{}
			if !baseline.known {
				continue
			}
			acknowledgementBaselineID := ""
			eventKey, reconstructed := transcriptBaselineBefore(
				baseline.events, acknowledgement.AcceptedAt, acknowledgement.BaselineCount,
			)
			if reconstructed {
				acknowledgementBaselineID = transcriptBaselineID(eventKey)
			} else if acknowledgement.BaselineKnown &&
				!transcriptHasUserEventAfter(baseline.events, acknowledgement.AcceptedAt) &&
				acknowledgement.BaselineCount == baseline.userEventCount {
				// A bounded transcript may have already evicted the acknowledgement's
				// baseline. Preserve the legacy count fallback only in that case.
				acknowledgementBaselineID = baseline.baselineID
			}
			if voiceBaselineMatches(
				acknowledgement.BaselineKnown, acknowledgement.BaselineCount,
				acknowledgementBaselineID, *baseline,
			) &&
				acknowledgement.Ordinal >= baseline.ordinal {
				baseline.ordinal = acknowledgement.Ordinal + 1
			}
		}
	}
	if len(unresolved) >= maxPendingVoices || baseline.ordinal > maxPendingVoices {
		return false, false
	}
	pending := newPendingVoice(operationID, baseline.ref, *baseline)
	h.pendingVoices[key] = append(h.pendingVoices[key], pending)
	return true, true
}

func resetVoiceBaselineGeneration(
	baseline *voicePendingBaseline,
	generation uint64,
	receivedAt time.Time,
) {
	baseline.generation = generation
	baseline.events = nil
	baseline.lastUserEvent = ""
	baseline.baselineID = ""
	baseline.userEventCount = 0
	baseline.ordinal = 1
	baseline.receivedAt = receivedAt
	baseline.known = false
}

func newPendingVoice(
	operationID string,
	ref domain.SessionRef,
	baseline voicePendingBaseline,
) voicePending {
	pending := voicePending{
		operationID: operationID, acceptedAt: baseline.receivedAt,
		baselineKnown: baseline.known && baseline.ref == ref,
		baselineCount: baseline.userEventCount,
		baselineID:    baseline.baselineID,
		ordinal:       baseline.ordinal, status: domain.OperationQueued,
	}
	if pending.acceptedAt.IsZero() {
		pending.acceptedAt = time.Now()
	}
	if pending.baselineKnown {
		pending.baselineEvent = baseline.lastUserEvent
	}
	return pending
}

func applyPendingVoiceBaseline(baseline *voicePendingBaseline, pending voicePending) {
	baseline.receivedAt = pending.acceptedAt
	baseline.known = pending.baselineKnown
	baseline.userEventCount = pending.baselineCount
	baseline.lastUserEvent = pending.baselineEvent
	baseline.baselineID = pending.baselineID
	baseline.ordinal = pending.ordinal
}

func (h *Handler) removePendingVoice(
	actor application.Principal,
	ref domain.SessionRef,
	generation uint64,
	operationID string,
) {
	key := voicePendingKey{userID: actor.UserID, ref: ref, generation: generation}
	h.voiceMu.Lock()
	queue := h.pendingVoices[key]
	remaining := queue[:0]
	for _, pending := range queue {
		if pending.operationID != operationID {
			remaining = append(remaining, pending)
		}
	}
	if len(remaining) == 0 {
		delete(h.pendingVoices, key)
	} else {
		h.pendingVoices[key] = remaining
	}
	h.voiceMu.Unlock()
}

func (h *Handler) hasPendingVoice(
	actor application.Principal,
	ref domain.SessionRef,
	generation uint64,
) bool {
	key := voicePendingKey{userID: actor.UserID, ref: ref, generation: generation}
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
	key := voicePendingKey{
		userID: actor.UserID, ref: ref, generation: session.RuntimeGeneration,
	}
	now := time.Now()

	h.voiceMu.Lock()
	h.sweepVoiceConfirmationsLocked(now)
	queue := h.pendingVoices[key]
	known := make(map[string]bool, len(queue))
	durableOrdinals := make(map[string]int)
	for _, pending := range queue {
		known[pending.operationID] = true
		if pending.baselineID != "" && pending.ordinal > durableOrdinals[pending.baselineID] {
			durableOrdinals[pending.baselineID] = pending.ordinal
		}
	}
	fallbackAt := time.Time{}
	for _, acknowledgement := range session.VoiceAcknowledgements {
		if known[acknowledgement.OperationID] ||
			h.voiceConfirmedLocked(key, acknowledgement.OperationID) {
			continue
		}
		if fallbackAt.IsZero() || acknowledgement.AcceptedAt.Before(fallbackAt) {
			fallbackAt = acknowledgement.AcceptedAt
		}
		baselineEvent := ""
		baselineID := ""
		if acknowledgement.BaselineKnown {
			if eventKey, reconstructed := transcriptBaselineBefore(
				events, acknowledgement.AcceptedAt, acknowledgement.BaselineCount,
			); reconstructed {
				baselineEvent = eventKey
				baselineID = transcriptBaselineID(eventKey)
			}
		}
		ordinal := acknowledgement.Ordinal
		if baselineID != "" {
			// Older builds could inflate the stored ordinal by counting an expired
			// acknowledgement that happened to have the same bounded event count.
			// Rebuild the ordinal from the exact transcript baseline on recovery.
			ordinal = durableOrdinals[baselineID] + 1
			durableOrdinals[baselineID] = ordinal
		}
		queue = append(queue, voicePending{
			operationID: acknowledgement.OperationID, acceptedAt: acknowledgement.AcceptedAt,
			status: acknowledgement.Status, baselineCount: acknowledgement.BaselineCount,
			baselineKnown: acknowledgement.BaselineKnown, baselineEvent: baselineEvent,
			baselineID: baselineID, ordinal: ordinal,
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
			} else if pending.baselineID != "" {
				after, found = transcriptUserEventsAfterID(events, pending.baselineID)
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
		if resolved {
			if h.rememberVoiceConfirmationLocked(key, pending.operationID, now) {
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
		if len(s.confirmedVoiceOrder)-remove <= maxVoiceConfirmations {
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

func transcriptUserEventsAfterID(events []transcript.Event, baselineID string) (int, bool) {
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
		found = transcriptBaselineID(transcriptEventKey(event)) == baselineID
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

func transcriptHasUserEventAfter(events []transcript.Event, after time.Time) bool {
	for _, event := range events {
		if event.Kind != transcript.EventUserText {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err == nil && at.After(after) {
			return true
		}
	}
	return false
}

func transcriptBaselineBefore(
	events []transcript.Event,
	acceptedAt time.Time,
	baselineCount int,
) (string, bool) {
	latestKey := ""
	latestAt := time.Time{}
	for _, event := range events {
		if event.Kind != transcript.EventUserText {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil || at.After(acceptedAt) || (!latestAt.IsZero() && at.Before(latestAt)) {
			continue
		}
		latestAt = at
		latestKey = transcriptEventKey(event)
	}
	if latestKey != "" {
		return latestKey, true
	}
	return "", baselineCount == 0
}

func transcriptEventKey(event transcript.Event) string {
	return event.Timestamp + "\x00" + event.Text
}

func transcriptBaselineID(eventKey string) string {
	if eventKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(eventKey))
	return hex.EncodeToString(digest[:])
}

func voiceBaselineMatches(
	known bool,
	count int,
	baselineID string,
	target voicePendingBaseline,
) bool {
	if !known || !target.known {
		return false
	}
	if baselineID != "" || target.baselineID != "" {
		return baselineID == target.baselineID
	}
	return count == target.userEventCount
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
