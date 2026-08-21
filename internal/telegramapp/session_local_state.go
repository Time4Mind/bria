package telegramapp

import (
	"sort"
	"strconv"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/processlog"
)

const (
	maxSessionPageStates      = 512
	maxPromptHashStates       = 512
	sessionLocalStateTTL      = 6 * time.Hour
	sessionLocalSweepInterval = time.Hour
)

func (h *Handler) storeSessionPageState(key sessionPageKey, state cardPageState) {
	now := time.Now()
	h.sessionStateMu.Lock()
	defer h.sessionStateMu.Unlock()
	h.ensureSessionLocalStateLocked()
	h.sessionPages[key] = state
	h.sessionPageTouched[key] = now
	trimOldestSessionPages(h.sessionPages, h.sessionPageTouched, maxSessionPageStates)
}

func (h *Handler) loadSessionPageState(key sessionPageKey) (cardPageState, bool) {
	now := time.Now()
	h.sessionStateMu.Lock()
	defer h.sessionStateMu.Unlock()
	h.ensureSessionLocalStateLocked()
	state, ok := h.sessionPages[key]
	if ok {
		h.sessionPageTouched[key] = now
	}
	return state, ok
}

func (h *Handler) storePromptHash(key sessionPageKey, hash string) {
	now := time.Now()
	h.sessionStateMu.Lock()
	defer h.sessionStateMu.Unlock()
	h.ensureSessionLocalStateLocked()
	h.promptHashes[key] = hash
	h.promptHashTouched[key] = now
	trimOldestPromptHashes(h.promptHashes, h.promptHashTouched, maxPromptHashStates)
}

func (h *Handler) loadPromptHash(key sessionPageKey) string {
	now := time.Now()
	h.sessionStateMu.Lock()
	defer h.sessionStateMu.Unlock()
	h.ensureSessionLocalStateLocked()
	hash, ok := h.promptHashes[key]
	if ok {
		h.promptHashTouched[key] = now
	}
	return hash
}

func (h *Handler) ensureSessionLocalStateLocked() {
	if h.sessionPages == nil {
		h.sessionPages = make(map[sessionPageKey]cardPageState)
	}
	if h.sessionPageTouched == nil {
		h.sessionPageTouched = make(map[sessionPageKey]time.Time)
	}
	if h.promptHashes == nil {
		h.promptHashes = make(map[sessionPageKey]string)
	}
	if h.promptHashTouched == nil {
		h.promptHashTouched = make(map[sessionPageKey]time.Time)
	}
}

func (h *Handler) maybeSweepSessionLocalState(now time.Time) {
	h.sessionStateMu.Lock()
	if !h.sessionStateSweepAt.IsZero() && now.Sub(h.sessionStateSweepAt) < sessionLocalSweepInterval {
		h.sessionStateMu.Unlock()
		return
	}
	h.sessionStateSweepAt = now
	h.sessionStateMu.Unlock()
	h.sweepSessionLocalState(now)
}

func (h *Handler) sweepSessionLocalState(now time.Time) {
	h.sessionStateMu.Lock()
	h.ensureSessionLocalStateLocked()
	keys := make(map[sessionPageKey]struct{}, len(h.sessionPages)+len(h.promptHashes))
	for key := range h.sessionPages {
		keys[key] = struct{}{}
	}
	for key := range h.promptHashes {
		keys[key] = struct{}{}
	}
	h.sessionStateMu.Unlock()

	invalid := make(map[sessionPageKey]bool)
	if h.service != nil {
		for key := range keys {
			ref := domain.SessionRef{NodeID: key.nodeID, SessionID: key.sessionID}
			_, err := h.service.Session(application.Principal{UserID: key.userID}, ref)
			invalid[key] = err != nil
		}
	}

	h.sessionStateMu.Lock()
	pagesBefore, promptsBefore := len(h.sessionPages), len(h.promptHashes)
	for key := range h.sessionPages {
		touched := h.sessionPageTouched[key]
		if invalid[key] || touched.IsZero() || now.Sub(touched) >= sessionLocalStateTTL {
			delete(h.sessionPages, key)
			delete(h.sessionPageTouched, key)
		}
	}
	for key := range h.promptHashes {
		touched := h.promptHashTouched[key]
		if invalid[key] || touched.IsZero() || now.Sub(touched) >= sessionLocalStateTTL {
			delete(h.promptHashes, key)
			delete(h.promptHashTouched, key)
		}
	}
	trimOldestSessionPages(h.sessionPages, h.sessionPageTouched, maxSessionPageStates)
	trimOldestPromptHashes(h.promptHashes, h.promptHashTouched, maxPromptHashStates)
	removedPages := pagesBefore - len(h.sessionPages)
	removedPrompts := promptsBefore - len(h.promptHashes)
	pageEntries, promptEntries := len(h.sessionPages), len(h.promptHashes)
	h.sessionStateMu.Unlock()
	if removedPages > 0 || removedPrompts > 0 {
		processlog.Detailf(
			"bria telegram: session_local_state outcome=cleaned pages=%d prompts=%d page_entries=%d prompt_entries=%d",
			removedPages, removedPrompts, pageEntries, promptEntries,
		)
	}
}

func trimOldestSessionPages(
	values map[sessionPageKey]cardPageState,
	touched map[sessionPageKey]time.Time,
	limit int,
) {
	trimOldestSessionKeys(len(values), limit, touched, func(key sessionPageKey) {
		delete(values, key)
		delete(touched, key)
	})
}

func trimOldestPromptHashes(
	values map[sessionPageKey]string,
	touched map[sessionPageKey]time.Time,
	limit int,
) {
	trimOldestSessionKeys(len(values), limit, touched, func(key sessionPageKey) {
		delete(values, key)
		delete(touched, key)
	})
}

func trimOldestSessionKeys(
	size int,
	limit int,
	touched map[sessionPageKey]time.Time,
	remove func(sessionPageKey),
) {
	if size <= limit {
		return
	}
	keys := make([]sessionPageKey, 0, len(touched))
	for key := range touched {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		leftAt, rightAt := touched[keys[left]], touched[keys[right]]
		if !leftAt.Equal(rightAt) {
			return leftAt.Before(rightAt)
		}
		return sessionPageKeyString(keys[left]) < sessionPageKeyString(keys[right])
	})
	for index := 0; index < size-limit && index < len(keys); index++ {
		remove(keys[index])
	}
}

func sessionPageKeyString(key sessionPageKey) string {
	return strconv.FormatInt(int64(key.userID), 10) + "\x00" +
		string(key.nodeID) + "\x00" + string(key.sessionID)
}
