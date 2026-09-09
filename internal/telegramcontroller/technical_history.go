package telegramcontroller

import (
	"bria/internal/cardtranscript"
	"bria/internal/controllerhistory"
	"bria/internal/domain"
	"bria/internal/finalpersist"
	"bria/internal/sessionruntime"
	"bria/internal/turnfinalization"
	"context"
)

func (c *Controller) persistFinal(ctx context.Context, id domain.SessionID, messageID, text string, binding domain.ProviderBinding) error {
	c.changeFinalWrite(id, 1)
	defer c.changeFinalWrite(id, -1)
	if store, ok := c.uiState.(finalpersist.Writer); ok {
		return turnfinalization.Save(ctx, finalpersist.Request{SessionID: id, Binding: binding, MessageID: messageID, Text: text, RequireRunning: c.turnLifecycle != nil}, c.sessions, store, c.ownerPrivateChatID, c.closeFlow.Observe, c.notify)
	}
	c.appendRuntimeHistoryForMessage(ctx, id, messageID, sessionruntime.TurnEvent{Kind: "final", Text: text})
	return nil
}

func (c *Controller) historyConsumer() *controllerhistory.History {
	return &controllerhistory.History{Mu: &c.mu, Store: c.uiState, Settings: c.settings, Values: &c.history, Kinds: &c.transcriptKinds, Technical: &c.technicalHistory, Prompts: &c.promptIndexes, Tails: &c.runtimeTail}
}
func (c *Controller) appendRuntimeHistory(ctx context.Context, id domain.SessionID, event sessionruntime.TurnEvent) {
	c.appendRuntimeHistoryForMessage(ctx, id, "", event)
}
func (c *Controller) appendRuntimeHistoryForMessage(ctx context.Context, id domain.SessionID, message string, event sessionruntime.TurnEvent) {
	c.historyConsumer().Append(ctx, id, message, event)
}
func (c *Controller) displayHistory(ctx context.Context, id domain.SessionID, history []string) ([]cardtranscript.Block, error) {
	return c.historyConsumer().Display(ctx, id, history)
}
func (c *Controller) persistRuntimeEvent(ctx context.Context, id domain.SessionID, message string, event sessionruntime.TurnEvent) error {
	return c.historyConsumer().PersistEvent(ctx, id, message, event)
}
