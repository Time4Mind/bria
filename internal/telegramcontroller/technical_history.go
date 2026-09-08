package telegramcontroller

import (
	"context"

	"bria/internal/cardtranscript"
	"bria/internal/controllertelemetry"
	"bria/internal/domain"
	"bria/internal/finalpersist"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontrolport"
)

type technicalHistoryStore = telegramcontrolport.TechnicalHistoryStore
type displayHistoryStore = telegramcontrolport.DisplayHistoryStore
type typedTranscriptStore = telegramcontrolport.TypedTranscriptStore
type typedTranscriptInserter = telegramcontrolport.TypedTranscriptInserter

func (c *Controller) persistFinal(ctx context.Context, id domain.SessionID, messageID, text string, binding domain.ProviderBinding) error {
	if store, ok := c.uiState.(finalpersist.Writer); ok {
		ctx = controllertelemetry.WithOperation(ctx, messageID+":final-save")
		retrying := false
		event := controllertelemetry.Event{Stage: controllertelemetry.FinalSave, OperationID: messageID + ":final-save", ParentOperationID: messageID, SessionID: string(id), Outcome: controllertelemetry.Failed, Reason: controllertelemetry.PersistFailed}
		err := finalpersist.Save(ctx, finalpersist.Request{SessionID: id, Binding: binding, MessageID: messageID, Text: text, RequireRunning: c.turnLifecycle != nil}, c.sessions, store, func(attempt uint64, failure error) {
			retrying = true
			c.closeFlow.Observe(context.WithoutCancel(ctx), event)
			if attempt == 1 {
				c.notify(ctx, Notification{OperationID: messageID + ":final-history-error", ConversationID: c.ownerPrivateChatID, SessionID: id, Kind: NotificationError, Text: "Не удалось сохранить финальный ответ. " + finalpersist.FailureText(failure) + " Повторю запись автоматически; очередь пока ожидает. Запрос модели не повторяется."})
			}
		})
		if retrying && err == nil {
			event.Outcome, event.Reason = controllertelemetry.Persisted, controllertelemetry.ReasonUnknown
			c.closeFlow.Observe(context.WithoutCancel(ctx), event)
			c.notify(ctx, Notification{OperationID: messageID + ":final-history-restored", ConversationID: c.ownerPrivateChatID, SessionID: id, Kind: NotificationPromptStatus, Text: "Финальный ответ сохранён. Завершаю запрос."})
		}
		return err
	}
	c.appendRuntimeHistoryForMessage(ctx, id, messageID, sessionruntime.TurnEvent{Kind: "final", Text: text})
	return nil
}

// Technical identity comes from the provider event type, never from visible
// text/emoji. Both displayed and hidden tool output remain in full history.
func (c *Controller) appendRuntimeHistory(ctx context.Context, id domain.SessionID, event sessionruntime.TurnEvent) {
	c.appendRuntimeHistoryForMessage(ctx, id, "", event)
}

func (c *Controller) appendRuntimeHistoryForMessage(ctx context.Context, id domain.SessionID, messageID string, event sessionruntime.TurnEvent) {
	if event.Kind == sessionruntime.EventTool && event.Metadata != nil {
		metadata := event.Metadata
		event.Text = cardtranscript.EncodeTool(cardtranscript.Tool{
			ID: metadata.ItemID, Name: metadata.Name, Arguments: metadata.Arguments,
			Output: metadata.Result, Status: metadata.Status,
		})
	}
	// Durable storage can locate the prompt by message ID even after a Bria
	// restart, when the in-memory prompt index has not been rebuilt yet.
	if messageID != "" {
		if store, ok := c.uiState.(typedTranscriptInserter); ok {
			if err := store.InsertCardTypedHistoryAfterPrompt(ctx, id, messageID, event.Text, string(event.Kind)); err == nil {
				if historyStore, ok := c.uiState.(CardHistoryStore); ok {
					if history, loadErr := historyStore.LoadCardHistory(ctx, id); loadErr == nil {
						c.mu.Lock()
						c.history[id] = history
						c.mu.Unlock()
					}
				}
				return
			}
		}
	}
	c.mu.Lock()
	if c.transcriptKinds == nil {
		c.transcriptKinds = make(map[domain.SessionID]map[int]string)
	}
	if c.transcriptKinds[id] == nil {
		c.transcriptKinds[id] = make(map[int]string)
	}
	kind := string(event.Kind)
	if messageID != "" {
		if promptIndex, ok := c.promptIndexes[id][messageID]; ok {
			insert := promptIndex + 1
			// Keep subsequent events of this turn together and shift all known
			// prompt indexes when inserting before queued prompts.
			if c.runtimeTail == nil {
				c.runtimeTail = make(map[domain.SessionID]map[string]int)
			}
			if c.runtimeTail[id] == nil {
				c.runtimeTail[id] = make(map[string]int)
			}
			if tail, ok := c.runtimeTail[id][messageID]; ok {
				insert = tail + 1
			}
			c.history[id] = append(c.history[id], "")
			copy(c.history[id][insert+1:], c.history[id][insert:])
			c.history[id][insert] = event.Text
			for key, index := range c.promptIndexes[id] {
				if index >= insert && key != messageID {
					c.promptIndexes[id][key] = index + 1
				}
			}
			for key, index := range c.runtimeTail[id] {
				if index >= insert {
					c.runtimeTail[id][key] = index + 1
				}
			}
			c.runtimeTail[id][messageID] = insert
			c.transcriptKinds[id][insert] = kind
			c.mu.Unlock()
			if store, ok := c.uiState.(CardHistoryStore); ok {
				_ = store.AppendCardHistory(ctx, id, event.Text)
			}
			return
		}
	}
	c.transcriptKinds[id][len(c.history[id])] = kind
	if event.Kind == sessionruntime.EventTool {
		if c.technicalHistory == nil {
			c.technicalHistory = make(map[domain.SessionID]map[int]bool)
		}
		if c.technicalHistory[id] == nil {
			c.technicalHistory[id] = make(map[int]bool)
		}
		c.technicalHistory[id][len(c.history[id])] = true
	}
	c.history[id] = append(c.history[id], event.Text)
	c.mu.Unlock()
	if store, ok := c.uiState.(typedTranscriptStore); ok {
		_ = store.AppendCardTypedHistory(ctx, id, event.Text, string(event.Kind))
		return
	}
	if event.Kind == sessionruntime.EventTool {
		if store, ok := c.uiState.(technicalHistoryStore); ok {
			_ = store.AppendCardTechnicalHistory(ctx, id, event.Text)
			return
		}
	}
	if store, ok := c.uiState.(CardHistoryStore); ok {
		_ = store.AppendCardHistory(ctx, id, event.Text)
	}
}

// Filter a projection copy before pagination. Settings changes can reveal old
// tool entries again without reconstructing history or resubmitting a turn.
func (c *Controller) displayHistory(ctx context.Context, id domain.SessionID, history []string) ([]cardtranscript.Block, error) {
	show, lines := true, 10
	if c.settings != nil {
		settings, err := c.settings.Snapshot(ctx)
		if err != nil {
			return nil, err
		}
		show = settings.ShowTechnicalActions
		lines = settings.TechnicalOutputLines
	}
	if store, ok := c.uiState.(typedTranscriptStore); ok {
		blocks, err := store.LoadCardTranscript(ctx, id, show)
		if err != nil {
			return nil, err
		}
		blocks = append([]cardtranscript.Block(nil), blocks...)
		for i := range blocks {
			blocks[i].ToolLines = lines
		}
		return cardtranscript.RenderBlocks(blocks), nil
	}
	if store, ok := c.uiState.(displayHistoryStore); ok {
		texts, err := store.LoadCardDisplayHistory(ctx, id, show)
		blocks := make([]cardtranscript.Block, len(texts))
		for i, text := range texts {
			blocks[i] = cardtranscript.Block{Text: text}
		}
		return blocks, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	blocks := make([]cardtranscript.Block, 0, len(history))
	for index, text := range history {
		if !show && c.technicalHistory[id][index] {
			continue
		}
		kind := c.transcriptKinds[id][index]
		for _, promptIndex := range c.promptIndexes[id] {
			if index == promptIndex {
				kind = "prompt"
				break
			}
		}
		blocks = append(blocks, cardtranscript.Block{Kind: kind, Text: text, ToolLines: lines})
	}
	return cardtranscript.RenderBlocks(blocks), nil
}
