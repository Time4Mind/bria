package telegramapp

import (
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const maxCachedPaneImages = 8

type paneCacheEntry struct {
	image      telegramui.PaneImage
	capturedAt time.Time
}

type paneRefreshState struct {
	paneMu         sync.Mutex
	paneGeneration map[domain.UserID]uint64
	paneWorkers    map[domain.UserID]uint64
	paneImages     map[string]paneCacheEntry
	paneImageOrder []string
}

func newPaneRefreshState() paneRefreshState {
	return paneRefreshState{
		paneGeneration: make(map[domain.UserID]uint64),
		paneWorkers:    make(map[domain.UserID]uint64),
		paneImages:     make(map[string]paneCacheEntry),
	}
}

func (s *paneRefreshState) rememberPaneImage(
	ref domain.SessionRef,
	image telegramui.PaneImage,
) {
	key := ref.Key()
	image.PNG = append([]byte(nil), image.PNG...)
	s.paneMu.Lock()
	defer s.paneMu.Unlock()
	for index, existing := range s.paneImageOrder {
		if existing == key {
			s.paneImageOrder = append(s.paneImageOrder[:index], s.paneImageOrder[index+1:]...)
			break
		}
	}
	s.paneImages[key] = paneCacheEntry{image: image, capturedAt: time.Now()}
	s.paneImageOrder = append(s.paneImageOrder, key)
	for len(s.paneImageOrder) > maxCachedPaneImages {
		oldest := s.paneImageOrder[0]
		s.paneImageOrder = s.paneImageOrder[1:]
		delete(s.paneImages, oldest)
	}
}

func (s *paneRefreshState) cachedPaneImage(
	ref domain.SessionRef,
	maxAge time.Duration,
) (telegramui.PaneImage, bool) {
	s.paneMu.Lock()
	defer s.paneMu.Unlock()
	entry, ok := s.paneImages[ref.Key()]
	if !ok || (maxAge > 0 && time.Since(entry.capturedAt) > maxAge) {
		return telegramui.PaneImage{}, false
	}
	entry.image.PNG = append([]byte(nil), entry.image.PNG...)
	return entry.image, true
}

type voicePendingState struct {
	voiceMu       sync.Mutex
	pendingVoices map[voicePendingKey][]voicePending
}

func newVoicePendingState() voicePendingState {
	return voicePendingState{pendingVoices: make(map[voicePendingKey][]voicePending)}
}

type cardRuntimeState struct {
	cardDataMu      sync.RWMutex
	cardContexts    map[string]cardContextEntry
	cardTranscripts map[string]cardTranscriptEntry
	cardMutationMu  sync.Mutex
}

func newCardRuntimeState() cardRuntimeState {
	return cardRuntimeState{
		cardContexts:    make(map[string]cardContextEntry),
		cardTranscripts: make(map[string]cardTranscriptEntry),
	}
}
