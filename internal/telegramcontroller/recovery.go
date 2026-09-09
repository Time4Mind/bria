package telegramcontroller

import (
	"context"
	"fmt"
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/telegramcontrolport"
	"bria/internal/telegramturnhelpers"
)

type SessionRecoverer = telegramcontrolport.SessionRecoverer

// RefreshRecoveryCard observes committed lifecycle state. The existing durable
// prompt refresh consumer suppresses non-active cards, preserving navigation.
func (c *Controller) RefreshRecoveryCard(ctx context.Context, id domain.SessionID) {
	current, err := c.sessions.Load(ctx, id)
	if err != nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if current.Status() == domain.SessionReady {
		c.live[id] = current
		c.ensureWorkerLocked(id)
	}
	c.mu.Unlock()
	telegramturnhelpers.WakeReadyInput(c.durableInput, current)
	// Recovery refreshes may arrive periodically while a legacy/native
	// attachment is unavailable. Do not append a new suppressed durable output
	// for every tick; only a committed Ready transition needs a card refresh.
	if current.Status() != domain.SessionReady {
		return
	}
	c.notify(ctx, Notification{OperationID: fmt.Sprintf("recovery-state:%s:%d", id, time.Now().UnixNano()), ConversationID: c.ownerPrivateChatID, SessionID: id, Kind: NotificationPromptStatus, Text: "session-state"})
}

// Recovery never submits the previous input. Unknown receipt outcomes are a
// visible, non-interactive history state, not a successful empty model answer.
func (c *Controller) resumeOrRecover(ctx context.Context, id domain.SessionID) (coordinator.Decision, error) {
	current, err := c.sessions.Load(ctx, id)
	if err != nil {
		return coordinator.Decision{}, err
	}
	if current.Status() != domain.SessionAwaitingRecovery {
		return c.ResumeArchived(ctx, id)
	}
	if current.ComputerID() != c.currentNodeID() {
		return coordinator.Decision{}, fmt.Errorf("recovery target belongs to another node")
	}
	if c.recoverer == nil {
		return c.cardDecision(ctx, id, "Восстановление недоступно. История сохранена; запрос не переотправлен.")
	}
	c.mu.Lock()
	if c.closed || c.recovering[id] {
		c.mu.Unlock()
		return c.cardDecision(ctx, id, "")
	}
	c.recovering[id] = true
	c.creates.Add(1)
	c.mu.Unlock()
	operation := controllertelemetry.Operation(ctx)
	workCtx := controllertelemetry.WithOperation(c.rootContext, operation)
	go func() {
		defer c.creates.Done()
		recovered, recoverErr := c.recoverer.RecoverSession(workCtx, id)
		stored, loadErr := c.sessions.Load(workCtx, id)
		notice := "Исход предыдущего запроса пока не подтверждён. История сохранена; запрос не переотправлен."
		if recoverErr == nil && loadErr == nil && recovered.Equal(stored) && recovered.ID() == id && recovered.Status() == domain.SessionReady {
			c.mu.Lock()
			updated := !c.closed
			if updated {
				c.live[id] = recovered
				c.page[id] = 0
				c.followLatest[id] = true
				c.ensureWorkerLocked(id)
			}
			c.mu.Unlock()
			if updated {
				telegramturnhelpers.WakeReadyInput(c.durableInput, recovered)
			}
			notice = "Сессия восстановлена без повторной отправки запроса."
		}
		c.mu.Lock()
		delete(c.recovering, id)
		c.mu.Unlock()
		c.notify(workCtx, Notification{OperationID: fmt.Sprintf("recovery:%s:%d", id, time.Now().UnixNano()), ConversationID: c.ownerPrivateChatID, SessionID: id, Kind: NotificationPromptStatus, Text: notice})
	}()
	return c.cardDecision(ctx, id, "Проверяю сохранённый исход запроса. Повторная отправка не выполняется.")
}
