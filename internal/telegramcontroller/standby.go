package telegramcontroller

import (
	"bria/internal/app"
	"bria/internal/domain"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

const standbyIntentPrefix = "standby:"

type emptyStandbyReader interface {
	HasEmptyCloseEligibility(context.Context, domain.SessionID) (bool, error)
}

type emptyStandbyRetirer interface {
	DeleteEmptyAwaitingRecovery(context.Context, domain.Session) (bool, error)
}

// ScheduleStandby is nonblocking and joins the controller's owned lifecycle.
// Overlapping triggers coalesce; failures are retained, never retried in a loop.
func (controller *Controller) ScheduleStandby() {
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return
	}
	controller.standbyRequested = true
	if !controller.standbyMu.TryLock() {
		controller.mu.Unlock()
		return
	}
	controller.creates.Add(1)
	controller.mu.Unlock()
	go func() {
		defer controller.creates.Done()
		for {
			controller.mu.Lock()
			controller.standbyRequested = false
			node := controller.currentNodeID()
			controller.mu.Unlock()
			err := controller.ensureStandby(controller.rootContext, node)
			controller.mu.Lock()
			if controller.standbyErrors == nil {
				controller.standbyErrors = make(map[domain.ComputerID]error)
			}
			controller.standbyErrors[node] = err
			if controller.closed || err != nil || !controller.standbyRequested {
				// Unlock under mu so a concurrent new trigger cannot be stranded
				// between the final pending check and releasing ownership.
				controller.standbyRequested = false
				controller.standbyMu.Unlock()
				controller.mu.Unlock()
				return
			}
			controller.mu.Unlock()
		}
	}()
}

// StandbyError provides a non-sensitive settings hint; internal errors are not
// interpolated because provider failures can carry private paths or output.
func (controller *Controller) StandbyError(node domain.ComputerID) string {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.standbyErrors[node] != nil {
		return "Не удалось подготовить ожидающую сессию. Повторите открытие раздела сессий."
	}
	return ""
}

func (controller *Controller) EnsureStandby(ctx context.Context, node domain.ComputerID) error {
	controller.standbyMu.Lock()
	defer controller.standbyMu.Unlock()
	return controller.ensureStandby(ctx, node)
}

func (controller *Controller) ensureStandby(ctx context.Context, node domain.ComputerID) error {
	if controller.settings == nil || node != controller.currentNodeID() {
		return nil
	}
	preferences, err := controller.settings.Snapshot(ctx)
	if err != nil {
		return err
	}
	if !preferences.StandbyEnabled {
		return nil
	}
	reader, ok := controller.sessions.(emptyStandbyReader)
	if !ok {
		return errors.New("standby requires durable empty-session evidence")
	}
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return err
	}
	used := make([]domain.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ComputerID() != node {
			continue
		}
		if !strings.HasPrefix(string(session.IntentID()), standbyIntentPrefix) {
			used = append(used, session)
			continue
		}
		empty, err := reader.HasEmptyCloseEligibility(ctx, session.ID())
		if err != nil {
			return err
		}
		if empty && session.Status() != domain.SessionArchived {
			if session.Status() == domain.SessionAwaitingRecovery {
				retirer, ok := controller.sessions.(emptyStandbyRetirer)
				if !ok {
					return errors.New("standby startup requires empty recovery retirement")
				}
				deleted, err := retirer.DeleteEmptyAwaitingRecovery(ctx, session)
				if err != nil {
					return err
				}
				if !deleted {
					return errors.New("standby empty recovery retirement was not confirmed")
				}
				continue
			}
			return nil
		} // Includes durable Starting/recovery: never duplicate uncertain launch.
		if !empty {
			used = append(used, session)
		}
	}
	computers, defaults, err := controller.creationStateV2(ctx)
	if err != nil {
		return err
	}
	if len(computers) != 1 || computers[0].ID != node {
		return errors.New("standby node unavailable")
	}
	draft := standbyTarget(node, used, computers[0].Capabilities, defaults.Providers[node])
	if draft.Provider == "" {
		return errors.New("standby has no enabled CLI")
	}
	if draft.Workdir == "" {
		draft.Workdir = defaults.Workdirs[node]
	}
	if draft.Workdir == "" {
		draft.Workdir, err = controller.creationHomeV2(ctx, node)
		if err != nil {
			return err
		}
	}
	if _, err := controller.browseCreationDirectoryV2(ctx, node, draft.Workdir); err != nil {
		return err
	}
	// The standby session is an ordinary session from the user's point of
	// view. Give it a stable, neutral name instead of the creation-flow label.
	// There is at most one standby session per node, so this remains unique.
	name := "default"
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	intent := app.ConfirmedSessionIntent{IntentID: domain.IntentID(standbyIntentPrefix + hex.EncodeToString(random[:])), ComputerID: node, Provider: draft.Provider, Workdir: draft.Workdir, Name: name}
	if node != controller.currentNodeID() {
		return nil
	}
	var result app.CreateSessionResult
	var selectionErr error
	if controller.asyncCreator != nil {
		pending, err := controller.asyncCreator.BeginCreate(ctx, intent)
		if err != nil {
			return err
		}
		if err := validatePendingCreate(pending, intent); err != nil {
			return err
		}
		controller.mu.Lock()
		controller.pending[pending.Session.ID()] = pending.Session
		selectPending := controller.active == "" && node == controller.currentNodeID()
		if selectPending {
			controller.active = pending.Session.ID()
		}
		controller.mu.Unlock()
		if selectPending {
			// The durable Starting session is already addressable, just like a
			// manually created session. Do not lose early prompts while CLI boots.
			// Still consume the owned outcome if selection persistence fails.
			selectionErr = controller.persistActive(ctx, pending.Session.ID())
		}
		if selectionErr == nil {
			// Starting is already durable. Publish it immediately even when an
			// older session remains active, so that card gets the new button while
			// provider startup continues in the background.
			controller.refreshStandbyCard(ctx, node, pending.Session.ID(), "starting")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case outcome, ok := <-pending.Outcome:
			if !ok {
				return errors.New("standby creation outcome missing")
			}
			if outcome.Err != nil {
				return outcome.Err
			}
			if !sameSessionIdentity(outcome.Session, pending.Session) {
				return errors.New("standby creation identity mismatch")
			}
			result = app.CreateSessionResult{Session: outcome.Session, Replayed: outcome.Replayed, StartError: outcome.StartError}
		}
	} else {
		result, err = controller.creator.Create(ctx, intent)
		if err != nil {
			return err
		}
	}
	session := result.Session
	if session.ID() == "" || session.IntentID() != intent.IntentID || session.ComputerID() != node || session.Provider() != intent.Provider || session.Workdir() != intent.Workdir {
		return errors.New("standby creation returned inconsistent session")
	}
	binding, bound := session.Binding()
	if session.Status() != domain.SessionReady || !bound || result.StartError != nil {
		return errors.New("standby CLI did not become ready")
	}
	controller.mu.Lock()
	delete(controller.pending, session.ID())
	controller.live[session.ID()] = session
	controller.ensureWorkerLocked(session.ID())
	if !result.Replayed {
		controller.created[session.ID()] = createdProcess{request: requestFromSession(session), binding: binding}
	}
	selectNew := controller.active == "" && node == controller.currentNodeID()
	if selectNew {
		controller.active = session.ID()
	}
	controller.mu.Unlock()
	if selectNew {
		if err := controller.persistActive(ctx, session.ID()); err != nil {
			return err
		}
	}
	controller.refreshStandbyCard(ctx, node, session.ID(), "ready")
	return selectionErr
}

func (controller *Controller) refreshStandbyCard(ctx context.Context, node domain.ComputerID, sessionID domain.SessionID, phase string) {
	controller.mu.Lock()
	active := controller.active
	visible := controller.nativeCardVisible
	controller.mu.Unlock()
	if visible && active != "" && node == controller.currentNodeID() {
		controller.notify(ctx, Notification{OperationID: "standby-" + phase + ":" + string(sessionID), ConversationID: controller.ownerPrivateChatID, SessionID: active, Kind: NotificationPromptStatus, Text: "session-state"})
	}
}

func (controller *Controller) standbyLabel(ctx context.Context, session domain.Session) string {
	if !strings.HasPrefix(string(session.IntentID()), standbyIntentPrefix) {
		return ""
	}
	reader, ok := controller.sessions.(emptyStandbyReader)
	if !ok {
		return ""
	}
	empty, err := reader.HasEmptyCloseEligibility(ctx, session.ID())
	if err != nil || !empty {
		return ""
	}
	if session.Name() != "" {
		return session.Name()
	}
	return "default"
}
