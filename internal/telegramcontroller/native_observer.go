package telegramcontroller

import (
	"context"
	"fmt"
	"time"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

// StartNativeObserver is called once after notification delivery has been
// bound. The worker is owned by Controller.Close, not a detached watcher.
func (c *Controller) StartNativeObserver() {
	reader, ok := c.native.(sessionruntime.NativeScreenProvider)
	if !ok {
		return
	}
	c.mu.Lock()
	if c.closed || c.nativeObserverStarted {
		c.mu.Unlock()
		return
	}
	c.nativeObserverStarted = true
	c.worker.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.worker.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		updates := reader.NativeScreenUpdates()
		for {
			select {
			case <-c.rootContext.Done():
				return
			case _, open := <-updates:
				if !open {
					updates = nil
				}
			case <-ticker.C:
			}
			c.refreshNativeObservation(c.rootContext, reader)
		}
	}()
}

// NativeScreenVisible is rechecked at delivery time: queued observations must
// never replace a menu/settings card opened after their publication.
func (c *Controller) NativeScreenVisible(id domain.SessionID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.nativeCardVisible && c.active == id
}

func (c *Controller) refreshNativeObservation(ctx context.Context, reader sessionruntime.NativeScreenProvider) {
	c.mu.Lock()
	id, visible := c.active, c.nativeCardVisible
	c.mu.Unlock()
	if id == "" || !visible {
		return
	}
	next, ok := reader.NativeScreen(id)
	if !ok || next.Hash == "" {
		return
	}
	c.mu.Lock()
	if c.closed || c.active != id || !c.nativeCardVisible {
		c.mu.Unlock()
		return
	}
	previous := c.nativeSnapshots[id]
	if previous.Hash == next.Hash && previous.Interactive == next.Interactive && previous.Model == next.Model {
		c.mu.Unlock()
		return
	}
	c.nativeSnapshots[id] = next
	if next.Interactive {
		c.nativeOverlay = id
	} else if c.nativeOverlay == id {
		c.nativeOverlay = ""
	}
	// Ordinary terminal changes only make the next rich_md card consume the
	// latest ready screenshot. They must not become screenshot-only Telegram
	// events. Interactive overlays still need a notification because their
	// native keyboard is part of the visible surface.
	changed := next.Interactive || previous.Interactive || previous.Model != next.Model
	c.mu.Unlock()
	if !changed {
		return
	}
	c.notify(ctx, Notification{OperationID: fmt.Sprintf("native-screen:%s:%d", id, time.Now().UnixNano()), ConversationID: c.ownerPrivateChatID, SessionID: id, Kind: NotificationNativeScreen, Text: "native screen changed"})
}
