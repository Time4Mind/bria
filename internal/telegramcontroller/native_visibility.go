package telegramcontroller

import (
	"context"
	"strings"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/viewdeliverycontext"
)

// Only user-driven projections change visibility. Background completion and
// ProjectCurrent cannot reopen a CLI picker over menus or close confirmation.
func (c *Controller) HandleSemanticAction(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	c.recordNativeVisibility(SemanticActionResult{})
	result, err := c.handleSemanticAction(ctx, action)
	c.projectedEvent(ctx, action.SessionID, result, err)
	if err == nil {
		c.recordNativeVisibility(result)
	}
	return result, err
}

func (c *Controller) HandleSemanticMessage(ctx context.Context, update coordinator.Update) (SemanticActionResult, error) {
	if update.ActorID != c.ownerUserID || update.ConversationID != c.ownerPrivateChatID || update.ConversationKind != "private" {
		return c.handleSemanticMessage(ctx, update)
	}
	// Ordinary input can publish prompt state before returning its projection.
	// Keep the existing view alive; slash commands may navigate away immediately.
	if strings.HasPrefix(strings.TrimSpace(update.Text), "/") {
		c.recordNativeVisibility(SemanticActionResult{})
	}
	result, err := c.handleSemanticMessage(ctx, update)
	if err == nil {
		c.recordNativeVisibility(result)
	}
	return result, err
}

func (c *Controller) recordNativeVisibility(result SemanticActionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nativeCardVisible = result.Card != nil && !result.Card.Recovery && !result.Card.CloseConfirmation && !result.Card.DeleteConfirmation || result.Surface != nil && result.Surface.NativeSessionID != ""
	if c.nativeCardVisible && c.nativeViewSession == c.active && c.nativeViewContext != nil && c.nativeViewContext.Err() == nil {
		return
	}
	c.nativeViewContext, c.nativeViewCancel = viewdeliverycontext.Renew(c.rootContext, c.nativeViewCancel, c.nativeCardVisible)
	c.nativeViewSession = c.active
}

// NativeDeliveryContext cancels only an observation's Telegram delivery when
// user navigation replaces the view. It never controls the provider process.
func (c *Controller) NativeDeliveryContext(parent context.Context, id domain.SessionID) (context.Context, context.CancelFunc, bool) {
	c.mu.Lock()
	view := c.nativeViewContext
	visible := !c.closed && c.nativeCardVisible && c.active == id && c.nativeViewSession == id && c.finalWrites[id] == 0 && view != nil
	c.mu.Unlock()
	if !visible {
		if c.rootContext.Err() == nil {
			return parent, func() {}, false
		}
		view = c.rootContext
	}
	ctx, cancel := viewdeliverycontext.Capture(parent, view, c.rootContext)
	return ctx, cancel, visible
}

// Invalidate projections during writes; durable pending state guards publication.
func (c *Controller) changeFinalWrite(id domain.SessionID, delta int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finalWrites[id] += delta
	if c.active != id {
		return
	}
	c.nativeViewContext, c.nativeViewCancel = viewdeliverycontext.Renew(c.rootContext, c.nativeViewCancel, !c.closed && c.nativeCardVisible && c.finalWrites[id] == 0)
	c.nativeViewSession = id
}
