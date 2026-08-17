package telegramapp

import (
	"context"
	"sync"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type cardContextEntry struct {
	percent  int
	present  bool
	revision uint64
}

type cardTranscriptEntry struct {
	providerSessionID string
	events            []transcript.Event
}

const maxCachedCardEvents = 200

func latestContextPercent(events []transcript.Event) (int, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].ContextPercent != nil {
			return *events[index].ContextPercent, true
		}
	}
	return 0, false
}

func (h *Handler) rememberCardTranscript(
	ref domain.SessionRef,
	revision uint64,
	providerSessionID string,
	events []transcript.Event,
) {
	h.cardDataMu.Lock()
	previous := h.cardTranscripts[ref.Key()]
	if previous.providerSessionID == providerSessionID {
		events = mergeCardTranscriptEvents(previous.events, events)
	} else {
		events = cloneTranscriptEvents(events)
	}
	h.cardTranscripts[ref.Key()] = cardTranscriptEntry{
		providerSessionID: providerSessionID,
		events:            events,
	}
	percent, present := latestContextPercent(events)
	h.cardContexts[ref.Key()] = cardContextEntry{
		percent: percent, present: present, revision: revision,
	}
	h.cardDataMu.Unlock()
}

func (h *Handler) cachedCardTranscript(ref domain.SessionRef) ([]transcript.Event, bool) {
	h.cardDataMu.RLock()
	entry, ok := h.cardTranscripts[ref.Key()]
	h.cardDataMu.RUnlock()
	return cloneTranscriptEvents(entry.events), ok
}

func mergeCardTranscriptEvents(
	previous []transcript.Event,
	current []transcript.Event,
) []transcript.Event {
	if len(previous) == 0 {
		return cloneTranscriptEvents(current)
	}
	if len(current) == 0 {
		return cloneTranscriptEvents(previous)
	}
	overlap := 0
	for count := min(len(previous), len(current)); count > 0; count-- {
		matched := true
		for index := 0; index < count; index++ {
			if !sameTranscriptEvent(
				previous[len(previous)-count+index], current[index],
			) {
				matched = false
				break
			}
		}
		if matched {
			overlap = count
			break
		}
	}
	merged := cloneTranscriptEvents(previous)
	if overlap > 0 {
		copy(merged[len(merged)-overlap:], cloneTranscriptEvents(current[:overlap]))
	}
	merged = append(merged, cloneTranscriptEvents(current[overlap:])...)
	if len(merged) > maxCachedCardEvents {
		merged = cloneTranscriptEvents(merged[len(merged)-maxCachedCardEvents:])
	}
	return merged
}

func sameTranscriptEvent(left, right transcript.Event) bool {
	return left.Kind == right.Kind && left.Text == right.Text &&
		left.ToolUseID == right.ToolUseID && left.ToolName == right.ToolName &&
		left.Head == right.Head && left.Body == right.Body &&
		left.Error == right.Error && left.Timestamp == right.Timestamp
}

func cloneTranscriptEvents(events []transcript.Event) []transcript.Event {
	cloned := append([]transcript.Event(nil), events...)
	for index := range cloned {
		if cloned[index].ContextPercent != nil {
			value := *cloned[index].ContextPercent
			cloned[index].ContextPercent = &value
		}
	}
	return cloned
}

func (h *Handler) cardContext(ref domain.SessionRef) application.CardContext {
	h.cardDataMu.RLock()
	active := h.cardContexts[ref.Key()]
	background := make(map[string]int, len(h.cardContexts))
	for key, entry := range h.cardContexts {
		if entry.present {
			background[key] = entry.percent
		}
	}
	h.cardDataMu.RUnlock()
	var activePercent *int
	if active.present {
		value := active.percent
		activePercent = &value
	}
	return application.CardContext{
		ActivePercent: activePercent, BackgroundPercent: background,
	}
}

func (h *Handler) cachedContextPercents() map[string]int {
	h.cardDataMu.RLock()
	result := make(map[string]int, len(h.cardContexts))
	for key, entry := range h.cardContexts {
		if entry.present {
			result[key] = entry.percent
		}
	}
	h.cardDataMu.RUnlock()
	return result
}

func (h *Handler) backgroundContextCurrent(ref domain.SessionRef, revision uint64) bool {
	h.cardDataMu.RLock()
	entry, ok := h.cardContexts[ref.Key()]
	h.cardDataMu.RUnlock()
	return ok && entry.revision == revision
}

func (h *Handler) backgroundContextValue(ref domain.SessionRef) (int, bool) {
	h.cardDataMu.RLock()
	entry, ok := h.cardContexts[ref.Key()]
	h.cardDataMu.RUnlock()
	return entry.percent, ok && entry.present
}

func (h *Handler) refreshBackgroundContexts(
	ctx context.Context,
	deliveries []application.BackgroundDelivery,
) {
	if h.controls == nil {
		return
	}
	semaphore := make(chan struct{}, backgroundSettleWorkers)
	var workers sync.WaitGroup
	for _, delivery := range deliveries {
		if h.backgroundContextCurrent(delivery.Session.Ref(), delivery.Notice.EventRevision) {
			continue
		}
		delivery := delivery
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			events, err := h.controls.Transcript(
				ctx, application.Principal{UserID: delivery.UserID}, delivery.Session.Ref(),
			)
			if err == nil {
				h.rememberCardTranscript(
					delivery.Session.Ref(), delivery.Notice.EventRevision,
					delivery.Session.ProviderSessionID, events,
				)
			}
		}()
	}
	workers.Wait()
}
