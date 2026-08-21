package telegramapp

import (
	"context"
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
	paneCancels    map[domain.UserID]context.CancelFunc
	paneImages     map[string]paneCacheEntry
	paneImageOrder []string
	paneCaptures   map[string]*paneCaptureFlight
}

type paneCaptureFlight struct {
	done chan struct{}
	pane []byte
	err  error
}

func newPaneRefreshState() paneRefreshState {
	return paneRefreshState{
		paneGeneration: make(map[domain.UserID]uint64),
		paneWorkers:    make(map[domain.UserID]uint64),
		paneCancels:    make(map[domain.UserID]context.CancelFunc),
		paneImages:     make(map[string]paneCacheEntry),
		paneCaptures:   make(map[string]*paneCaptureFlight),
	}
}

func (s *paneRefreshState) rememberPaneImage(
	ref domain.SessionRef,
	image telegramui.PaneImage,
) telegramui.PaneImage {
	key := ref.Key()
	image.PNG = append([]byte(nil), image.PNG...)
	s.paneMu.Lock()
	defer s.paneMu.Unlock()
	if previous, ok := s.paneImages[key]; ok &&
		previous.image.Hash == image.Hash && previous.image.FileID != "" {
		// Rendering the current terminal produced the same image. Keep the
		// Telegram-side upload instead of uploading identical PNG bytes again.
		image.PNG = nil
		image.FileID = previous.image.FileID
	}
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
	image.PNG = append([]byte(nil), image.PNG...)
	return image
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

func (s *paneRefreshState) rememberPaneFileID(
	ref domain.SessionRef,
	hash string,
	fileID string,
) {
	if hash == "" || fileID == "" {
		return
	}
	s.paneMu.Lock()
	defer s.paneMu.Unlock()
	entry, ok := s.paneImages[ref.Key()]
	if !ok || entry.image.Hash != hash {
		return
	}
	entry.image.PNG = nil
	entry.image.FileID = fileID
	s.paneImages[ref.Key()] = entry
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
	settledCards    map[domain.UserID]settledCardCheck
	cardCacheOrder  []string
	cardCacheHits   uint64
	cardCacheMisses uint64
	cardEvictions   uint64
	transcriptReads uint64
	transcriptSlow  uint64
	transcriptTotal time.Duration
	transcriptMax   time.Duration
	cardMutationMu  sync.Mutex
}

func newCardRuntimeState() cardRuntimeState {
	return cardRuntimeState{
		cardContexts:    make(map[string]cardContextEntry),
		cardTranscripts: make(map[string]cardTranscriptEntry),
		settledCards:    make(map[domain.UserID]settledCardCheck),
	}
}
