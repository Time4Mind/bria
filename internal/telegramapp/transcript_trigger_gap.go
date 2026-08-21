package telegramapp

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/transcript"
)

const (
	transcriptTriggerGrace     = 5 * time.Second
	maxTranscriptTriggerStates = 512
)

type transcriptRefreshKey struct {
	ref               domain.SessionRef
	generation        uint64
	providerSessionID string
}

type transcriptFinalKey struct {
	at     time.Time
	digest [sha256.Size]byte
}

type transcriptEventMark struct {
	digest    [sha256.Size]byte
	kind      transcript.EventKind
	timestamp string
}

type transcriptPendingGap struct {
	final        transcriptFinalKey
	events       []transcriptEventMark
	discoveredAt time.Time
	source       string
}

type transcriptTriggerSession struct {
	confirmedFinal  transcriptFinalKey
	confirmedEvents []transcriptEventMark
	pending         *transcriptPendingGap
	reportedFinal   transcriptFinalKey
}

type transcriptTriggerTracker struct {
	mu        sync.Mutex
	startedAt time.Time
	sessions  map[transcriptRefreshKey]*transcriptTriggerSession
	order     []transcriptRefreshKey
}

type transcriptTriggerGap struct {
	ref           domain.SessionRef
	generation    uint64
	source        string
	missingEvents int
	kinds         string
	firstAt       string
	lastAt        string
	discovery     time.Duration
	deltaComplete bool
}

func newTranscriptTriggerTracker(startedAt time.Time) transcriptTriggerTracker {
	return transcriptTriggerTracker{
		startedAt: startedAt,
		sessions:  make(map[transcriptRefreshKey]*transcriptTriggerSession),
	}
}

func transcriptKey(session domain.Session) transcriptRefreshKey {
	return transcriptRefreshKey{
		ref: session.Ref(), generation: session.RuntimeGeneration,
		providerSessionID: session.ProviderSessionID,
	}
}

func transcriptFinalIdentity(event transcript.Event) transcriptFinalKey {
	encoded, _ := json.Marshal(event)
	at, _ := time.Parse(time.RFC3339Nano, event.Timestamp)
	return transcriptFinalKey{at: at, digest: sha256.Sum256(encoded)}
}

func transcriptEventMarks(events []transcript.Event) []transcriptEventMark {
	result := make([]transcriptEventMark, 0, len(events))
	for _, event := range events {
		encoded, _ := json.Marshal(event)
		result = append(result, transcriptEventMark{
			digest: sha256.Sum256(encoded), kind: event.Kind, timestamp: event.Timestamp,
		})
	}
	return result
}

func currentTurnTranscriptEvents(
	session domain.Session,
	events []transcript.Event,
) []transcript.Event {
	operation := session.LastOperation
	if operation == nil || operation.Action != domain.ActionSendInput {
		if len(events) == 0 {
			return nil
		}
		finalIndex := -1
		for index := len(events) - 1; index >= 0; index-- {
			if events[index].Kind == transcript.EventAssistantFinal {
				finalIndex = index
				break
			}
		}
		if finalIndex < 0 {
			return events[len(events)-1:]
		}
		start := finalIndex
		for index := finalIndex - 1; index >= 0; index-- {
			if events[index].Kind == transcript.EventUserText {
				start = index
				break
			}
		}
		return events[start:]
	}
	boundary := operation.At.Add(-currentTurnUserTimestampSkew)
	start := -1
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Kind != transcript.EventUserText {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, events[index].Timestamp)
		if err == nil && !at.Before(boundary) {
			start = index
			break
		}
	}
	if start < 0 {
		for index, event := range events {
			at, err := time.Parse(time.RFC3339Nano, event.Timestamp)
			if err == nil && !at.Before(boundary) {
				start = index
				break
			}
		}
	}
	if start < 0 {
		if len(events) == 0 {
			return nil
		}
		start = len(events) - 1
	}
	return events[start:]
}

func (tracker *transcriptTriggerTracker) confirm(
	session domain.Session,
	events []transcript.Event,
	now time.Time,
) {
	key := transcriptKey(session)
	turnEvents := currentTurnTranscriptEvents(session, events)
	marks := transcriptEventMarks(turnEvents)
	final, finalOK := finalTranscriptEvent(events)
	finalAt, parsed := finalTranscriptAt(events)
	currentFinal := finalOK && parsed && transcriptFinalBelongsToCurrentTurn(session, finalAt, now)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	state := tracker.sessionLocked(key)
	state.confirmedEvents = marks
	if currentFinal {
		state.confirmedFinal = transcriptFinalIdentity(final)
		if state.pending != nil && state.pending.final == state.confirmedFinal {
			state.pending = nil
		}
	} else {
		state.confirmedFinal = transcriptFinalKey{}
	}
	tracker.touchLocked(key)
	tracker.evictLocked()
}

func (tracker *transcriptTriggerTracker) observeWatchdog(
	session domain.Session,
	events []transcript.Event,
	source string,
	now time.Time,
) {
	final, finalOK := finalTranscriptEvent(events)
	finalAt, parsed := finalTranscriptAt(events)
	if !finalOK || !parsed || finalAt.Before(tracker.startedAt) ||
		!transcriptFinalBelongsToCurrentTurn(session, finalAt, now) {
		return
	}
	key := transcriptKey(session)
	identity := transcriptFinalIdentity(final)
	marks := transcriptEventMarks(currentTurnTranscriptEvents(session, events))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	state := tracker.sessionLocked(key)
	if state.confirmedFinal == identity || state.reportedFinal == identity ||
		(state.pending != nil && state.pending.final == identity) {
		return
	}
	state.pending = &transcriptPendingGap{
		final: identity, events: marks, discoveredAt: now, source: source,
	}
	tracker.touchLocked(key)
	tracker.evictLocked()
}

func (tracker *transcriptTriggerTracker) flushDue(now time.Time) []transcriptTriggerGap {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	result := make([]transcriptTriggerGap, 0)
	for key, state := range tracker.sessions {
		pending := state.pending
		if pending == nil || now.Sub(pending.discoveredAt) < transcriptTriggerGrace {
			continue
		}
		state.pending = nil
		if state.confirmedFinal == pending.final || state.reportedFinal == pending.final {
			continue
		}
		state.reportedFinal = pending.final
		delta, complete := transcriptMarkDelta(state.confirmedEvents, pending.events)
		result = append(result, newTranscriptTriggerGap(key, pending, delta, complete))
	}
	tracker.evictLocked()
	return result
}

func (tracker *transcriptTriggerTracker) sessionLocked(
	key transcriptRefreshKey,
) *transcriptTriggerSession {
	if tracker.sessions == nil {
		tracker.sessions = make(map[transcriptRefreshKey]*transcriptTriggerSession)
	}
	state := tracker.sessions[key]
	if state == nil {
		state = &transcriptTriggerSession{}
		tracker.sessions[key] = state
	}
	return state
}

func (tracker *transcriptTriggerTracker) touchLocked(key transcriptRefreshKey) {
	for index, existing := range tracker.order {
		if existing == key {
			tracker.order = append(tracker.order[:index], tracker.order[index+1:]...)
			break
		}
	}
	tracker.order = append(tracker.order, key)
}

func (tracker *transcriptTriggerTracker) evictLocked() {
	for len(tracker.sessions) > maxTranscriptTriggerStates {
		victim := -1
		for index, key := range tracker.order {
			if state := tracker.sessions[key]; state == nil || state.pending == nil {
				victim = index
				break
			}
		}
		if victim < 0 {
			return
		}
		key := tracker.order[victim]
		tracker.order = append(tracker.order[:victim], tracker.order[victim+1:]...)
		delete(tracker.sessions, key)
	}
}

func transcriptMarkDelta(
	previous []transcriptEventMark,
	current []transcriptEventMark,
) ([]transcriptEventMark, bool) {
	if len(previous) == 0 {
		return append([]transcriptEventMark(nil), current...), false
	}
	for count := min(len(previous), len(current)); count > 0; count-- {
		matched := true
		for index := 0; index < count; index++ {
			if previous[len(previous)-count+index].digest != current[index].digest {
				matched = false
				break
			}
		}
		if matched {
			return append([]transcriptEventMark(nil), current[count:]...), true
		}
	}
	return append([]transcriptEventMark(nil), current...), false
}

func newTranscriptTriggerGap(
	key transcriptRefreshKey,
	pending *transcriptPendingGap,
	delta []transcriptEventMark,
	complete bool,
) transcriptTriggerGap {
	kindCounts := make(map[string]int)
	for _, event := range delta {
		kindCounts[string(event.kind)]++
	}
	kinds := make([]string, 0, len(kindCounts))
	for kind, count := range kindCounts {
		kinds = append(kinds, kind+":"+strconv.Itoa(count))
	}
	sort.Strings(kinds)
	firstAt, lastAt := "", ""
	if len(delta) > 0 {
		firstAt = delta[0].timestamp
		lastAt = delta[len(delta)-1].timestamp
	}
	discovery := pending.discoveredAt.Sub(pending.final.at)
	if discovery < 0 {
		discovery = 0
	}
	return transcriptTriggerGap{
		ref: key.ref, generation: key.generation, source: pending.source,
		missingEvents: len(delta), kinds: strings.Join(kinds, ","),
		firstAt: firstAt, lastAt: lastAt, discovery: discovery,
		deltaComplete: complete,
	}
}

func (h *Handler) confirmTranscriptTrigger(
	session domain.Session,
	events []transcript.Event,
) {
	h.transcriptTriggers.confirm(session, events, time.Now())
}

func (h *Handler) observeTranscriptWatchdog(
	session domain.Session,
	events []transcript.Event,
	source string,
	now time.Time,
) {
	h.transcriptTriggers.observeWatchdog(session, events, source, now)
}

func (h *Handler) flushTranscriptTriggerGaps(now time.Time) {
	for _, gap := range h.transcriptTriggers.flushDue(now) {
		processlog.Failuref(
			processlog.Service, processlog.FailureConsistency,
			"bria telegram: transcript_trigger_gap ref=%q generation=%d source=%s "+
				"missing_events=%d event_kinds=%q first_at=%q last_at=%q "+
				"discovery_ms=%d delta_complete=%t",
			gap.ref.Key(), gap.generation, gap.source, gap.missingEvents, gap.kinds,
			gap.firstAt, gap.lastAt, gap.discovery.Milliseconds(), gap.deltaComplete,
		)
	}
}
