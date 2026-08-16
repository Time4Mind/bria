package telegramapp

import (
	"sync"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type paneRefreshState struct {
	paneMu         sync.Mutex
	paneGeneration map[domain.UserID]uint64
	paneWorkers    map[domain.UserID]uint64
}

func newPaneRefreshState() paneRefreshState {
	return paneRefreshState{
		paneGeneration: make(map[domain.UserID]uint64),
		paneWorkers:    make(map[domain.UserID]uint64),
	}
}

type voicePendingState struct {
	voiceMu       sync.Mutex
	pendingVoices map[voicePendingKey][]voicePending
}

func newVoicePendingState() voicePendingState {
	return voicePendingState{pendingVoices: make(map[voicePendingKey][]voicePending)}
}

type inputPendingState struct {
	inputMu       sync.Mutex
	pendingInputs map[inputPendingKey][]inputPending
}

func newInputPendingState() inputPendingState {
	return inputPendingState{pendingInputs: make(map[inputPendingKey][]inputPending)}
}

type cardRuntimeState struct {
	cardDataMu      sync.RWMutex
	cardContexts    map[string]cardContextEntry
	cardTranscripts map[string][]transcript.Event
	cardMutationMu  sync.Mutex
}

func newCardRuntimeState() cardRuntimeState {
	return cardRuntimeState{
		cardContexts:    make(map[string]cardContextEntry),
		cardTranscripts: make(map[string][]transcript.Event),
	}
}
