package telegramcontroller

import (
	"context"

	"bria/internal/coordinator"
	"bria/internal/domain"
)

// Only user-driven projections change visibility. Background completion and
// ProjectCurrent cannot reopen a CLI picker over menus or close confirmation.
func (c *Controller) HandleSemanticAction(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	c.hideNativeVisibility()
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
	c.hideNativeVisibility()
	result, err := c.handleSemanticMessage(ctx, update)
	if err == nil {
		c.recordNativeVisibility(result)
	}
	return result, err
}

func (c *Controller) recordNativeVisibility(result SemanticActionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nativeCardVisible = result.Card != nil && !result.Card.Recovery && !result.Card.CloseConfirmation && !result.Card.DeleteConfirmation
	if result.Surface != nil && result.Surface.NativeSessionID != "" {
		c.nativeCardVisible = true
	}
	if c.nativeCardVisible {
		c.nativeViewContext, c.nativeViewCancel = context.WithCancel(c.rootContext)
	}
}

func (c *Controller) hideNativeVisibility() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nativeCardVisible = false
	if c.nativeViewCancel != nil {
		c.nativeViewCancel()
	}
	c.nativeViewContext, c.nativeViewCancel = nil, nil
}

// NativeDeliveryContext cancels only an observation's Telegram delivery when
// user navigation replaces the view. It never controls the provider process.
func (c *Controller) NativeDeliveryContext(parent context.Context, id domain.SessionID) (context.Context, context.CancelFunc, bool) {
	c.mu.Lock()
	view := c.nativeViewContext
	visible := !c.closed && c.nativeCardVisible && c.active == id && view != nil
	c.mu.Unlock()
	if !visible || view.Err() != nil {
		return parent, func() {}, false
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(view, cancel)
	if view.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }, true
}
