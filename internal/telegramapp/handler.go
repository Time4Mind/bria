package telegramapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/telegramui"
)

var ErrInconsistentCreationResult = errors.New("session creator returned an inconsistent result")

var ErrInvalidUpdateID = errors.New("authorized Telegram update id must be positive")

type ChatKind string

const (
	ChatPrivate ChatKind = "private"
	ChatGroup   ChatKind = "group"
	ChatChannel ChatKind = "channel"
)

type ConfirmedSessionCreation struct {
	UpdateID     int64
	SenderUserID int64
	ChatID       int64
	ChatKind     ChatKind
	Intent       app.ConfirmedSessionIntent
}

type CreationDisposition string

const (
	DispositionIgnoredUnauthorized CreationDisposition = "ignored_unauthorized"
	DispositionCreatedReady        CreationDisposition = "created_ready"
	DispositionReplayed            CreationDisposition = "replayed"
	DispositionAwaitingRecovery    CreationDisposition = "awaiting_recovery"
)

type CreationReceipt struct {
	Disposition     CreationDisposition
	UpdateID        int64
	IntentID        domain.IntentID
	SessionID       domain.SessionID
	Status          domain.SessionStatus
	ProviderBinding *domain.ProviderBinding
	Card            *telegramui.SessionCard
}

type SessionCreator interface {
	Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error)
}

type Handler struct {
	ownerUserID        int64
	ownerPrivateChatID int64
	creator            SessionCreator
}

func NewHandler(
	ownerUserID int64,
	ownerPrivateChatID int64,
	creator SessionCreator,
) (*Handler, error) {
	if ownerUserID <= 0 || ownerPrivateChatID <= 0 || creator == nil {
		return nil, errors.New("owner user id, owner private chat id, and session creator are required")
	}
	return &Handler{
		ownerUserID:        ownerUserID,
		ownerPrivateChatID: ownerPrivateChatID,
		creator:            creator,
	}, nil
}

func (handler *Handler) HandleConfirmedSessionCreation(
	ctx context.Context,
	event ConfirmedSessionCreation,
) (CreationReceipt, error) {
	if event.SenderUserID != handler.ownerUserID ||
		event.ChatKind != ChatPrivate ||
		event.ChatID != handler.ownerPrivateChatID {
		return CreationReceipt{Disposition: DispositionIgnoredUnauthorized}, nil
	}
	if event.UpdateID <= 0 {
		return CreationReceipt{}, fmt.Errorf("%w: %d", ErrInvalidUpdateID, event.UpdateID)
	}

	intent := event.Intent
	intent.IntentID = domain.IntentID("telegram-update:" + strconv.FormatInt(event.UpdateID, 10))
	result, err := handler.creator.Create(ctx, intent)
	if err != nil {
		return CreationReceipt{}, fmt.Errorf("create confirmed session: %w", err)
	}
	if !sessionMatchesIntent(result.Session, intent) {
		return CreationReceipt{}, fmt.Errorf(
			"%w: persisted session does not match confirmed intent",
			ErrInconsistentCreationResult,
		)
	}

	binding, hasBinding := result.Session.Binding()
	card := telegramui.ProjectSessionCard(result.Session)
	receipt := CreationReceipt{
		UpdateID:  event.UpdateID,
		IntentID:  result.Session.IntentID(),
		SessionID: result.Session.ID(),
		Status:    result.Session.Status(),
		Card:      &card,
	}
	if hasBinding {
		receipt.ProviderBinding = &binding
	}

	switch {
	case result.Replayed:
		if result.StartError != nil {
			return CreationReceipt{}, fmt.Errorf(
				"%w: replayed result contains a new start error",
				ErrInconsistentCreationResult,
			)
		}
		receipt.Disposition = DispositionReplayed
		return receipt, nil
	case result.StartError != nil:
		if result.Session.Status() != domain.SessionAwaitingRecovery || hasBinding {
			return CreationReceipt{}, fmt.Errorf(
				"%w: start failure is not awaiting recovery",
				ErrInconsistentCreationResult,
			)
		}
		receipt.Disposition = DispositionAwaitingRecovery
		return receipt, nil
	case result.Session.Status() != domain.SessionReady || !hasBinding:
		return CreationReceipt{}, fmt.Errorf(
			"%w: new session is not ready with a provider binding",
			ErrInconsistentCreationResult,
		)
	default:
		receipt.Disposition = DispositionCreatedReady
		return receipt, nil
	}
}

func sessionMatchesIntent(session domain.Session, intent app.ConfirmedSessionIntent) bool {
	return session.IntentID() == intent.IntentID &&
		session.ComputerID() == intent.ComputerID &&
		session.Provider() == intent.Provider &&
		session.Workdir() == intent.Workdir
}
