// Package controllerhistory owns typed runtime history persistence and display projection.
package controllerhistory

import (
	"bria/internal/cardtranscript"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontrolport"
	"context"
	"sync"
)

// History borrows controller-owned maps and their existing lock; it adds no state
// schema or independent lifecycle. Construct it for the current controller view.
type History struct {
	Mu        sync.Locker
	Store     any
	Settings  telegramcontrolport.Preferences
	Values    *map[domain.SessionID][]string
	Kinds     *map[domain.SessionID]map[int]string
	Technical *map[domain.SessionID]map[int]bool
	Prompts   *map[domain.SessionID]map[string]int
	Tails     *map[domain.SessionID]map[string]int
}

func (h *History) Append(ctx context.Context, id domain.SessionID, messageID string, event sessionruntime.TurnEvent) {
	event.Text = Text(event)
	// Durable storage can locate the prompt by message ID even after a Bria
	// restart, when the in-memory prompt index has not been rebuilt yet.
	if messageID != "" {
		if store, ok := h.Store.(telegramcontrolport.TypedTranscriptInserter); ok {
			if err := store.InsertCardTypedHistoryAfterPrompt(ctx, id, messageID, event.Text, string(event.Kind)); err == nil {
				if historyStore, ok := h.Store.(telegramcontrolport.CardHistoryStore); ok {
					if history, loadErr := historyStore.LoadCardHistory(ctx, id); loadErr == nil {
						h.Mu.Lock()
						(*h.Values)[id] = history
						h.Mu.Unlock()
					}
				}
				return
			}
		}
	}
	h.Mu.Lock()
	if (*h.Kinds) == nil {
		(*h.Kinds) = make(map[domain.SessionID]map[int]string)
	}
	if (*h.Kinds)[id] == nil {
		(*h.Kinds)[id] = make(map[int]string)
	}
	kind := string(event.Kind)
	if messageID != "" {
		if promptIndex, ok := (*h.Prompts)[id][messageID]; ok {
			insert := promptIndex + 1
			// Keep subsequent events of this turn together and shift all known
			// prompt indexes when inserting before queued prompts.
			if (*h.Tails) == nil {
				(*h.Tails) = make(map[domain.SessionID]map[string]int)
			}
			if (*h.Tails)[id] == nil {
				(*h.Tails)[id] = make(map[string]int)
			}
			if tail, ok := (*h.Tails)[id][messageID]; ok {
				insert = tail + 1
			}
			(*h.Values)[id] = append((*h.Values)[id], "")
			copy((*h.Values)[id][insert+1:], (*h.Values)[id][insert:])
			(*h.Values)[id][insert] = event.Text
			for key, index := range (*h.Prompts)[id] {
				if index >= insert && key != messageID {
					(*h.Prompts)[id][key] = index + 1
				}
			}
			for key, index := range (*h.Tails)[id] {
				if index >= insert {
					(*h.Tails)[id][key] = index + 1
				}
			}
			(*h.Tails)[id][messageID] = insert
			(*h.Kinds)[id][insert] = kind
			h.Mu.Unlock()
			if store, ok := h.Store.(telegramcontrolport.CardHistoryStore); ok {
				_ = store.AppendCardHistory(ctx, id, event.Text)
			}
			return
		}
	}
	(*h.Kinds)[id][len((*h.Values)[id])] = kind
	if event.Kind == sessionruntime.EventTool {
		if (*h.Technical) == nil {
			(*h.Technical) = make(map[domain.SessionID]map[int]bool)
		}
		if (*h.Technical)[id] == nil {
			(*h.Technical)[id] = make(map[int]bool)
		}
		(*h.Technical)[id][len((*h.Values)[id])] = true
	}
	(*h.Values)[id] = append((*h.Values)[id], event.Text)
	h.Mu.Unlock()
	if store, ok := h.Store.(telegramcontrolport.TypedTranscriptStore); ok {
		_ = store.AppendCardTypedHistory(ctx, id, event.Text, string(event.Kind))
		return
	}
	if event.Kind == sessionruntime.EventTool {
		if store, ok := h.Store.(telegramcontrolport.TechnicalHistoryStore); ok {
			_ = store.AppendCardTechnicalHistory(ctx, id, event.Text)
			return
		}
	}
	if store, ok := h.Store.(telegramcontrolport.CardHistoryStore); ok {
		_ = store.AppendCardHistory(ctx, id, event.Text)
	}
}

// Filter a projection copy before pagination. Settings changes can reveal old
// tool entries again without reconstructing history or resubmitting a turn.
func (h *History) Display(ctx context.Context, id domain.SessionID, history []string) ([]cardtranscript.Block, error) {
	blocks, _, err := h.DisplayWithActivity(ctx, id, history)
	return blocks, err
}

func (h *History) DisplayWithActivity(ctx context.Context, id domain.SessionID, history []string) ([]cardtranscript.Block, int64, error) {
	show, lines, commandLines := true, 10, 10
	if h.Settings != nil {
		settings, err := h.Settings.Snapshot(ctx)
		if err != nil {
			return nil, 0, err
		}
		show = settings.ShowTechnicalActions
		lines = settings.TechnicalOutputLines
		commandLines = settings.TechnicalCommandLines
	}
	if store, ok := h.Store.(telegramcontrolport.TypedTranscriptSnapshotStore); ok {
		snapshot, err := store.LoadCardTranscriptSnapshot(ctx, id, show)
		if err != nil {
			return nil, 0, err
		}
		blocks := append([]cardtranscript.Block(nil), snapshot.Blocks...)
		for i := range blocks {
			blocks[i].ToolLines = lines
			blocks[i].CommandLines = commandLines
		}
		return cardtranscript.RenderBlocks(blocks), snapshot.LastEventUnixNano, nil
	}
	if store, ok := h.Store.(telegramcontrolport.TypedTranscriptStore); ok {
		blocks, err := store.LoadCardTranscript(ctx, id, show)
		if err != nil {
			return nil, 0, err
		}
		blocks = append([]cardtranscript.Block(nil), blocks...)
		for i := range blocks {
			blocks[i].ToolLines = lines
			blocks[i].CommandLines = commandLines
		}
		return cardtranscript.RenderBlocks(blocks), 0, nil
	}
	if store, ok := h.Store.(telegramcontrolport.DisplayHistoryStore); ok {
		texts, err := store.LoadCardDisplayHistory(ctx, id, show)
		blocks := make([]cardtranscript.Block, len(texts))
		for i, text := range texts {
			blocks[i] = cardtranscript.Block{Text: text}
		}
		return blocks, 0, err
	}
	h.Mu.Lock()
	defer h.Mu.Unlock()
	blocks := make([]cardtranscript.Block, 0, len(history))
	for index, text := range history {
		if !show && (*h.Technical)[id][index] {
			continue
		}
		kind := (*h.Kinds)[id][index]
		for _, promptIndex := range (*h.Prompts)[id] {
			if index == promptIndex {
				kind = "prompt"
				break
			}
		}
		blocks = append(blocks, cardtranscript.Block{Kind: kind, Text: text, ToolLines: lines, CommandLines: commandLines})
	}
	return cardtranscript.RenderBlocks(blocks), 0, nil
}
