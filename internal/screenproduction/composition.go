// Package screenproduction projects typed provider events into the bounded
// virtual screen and, when enabled globally, sends its PNG to Telegram.
package screenproduction

import (
	"context"
	"errors"
	"strings"
	"sync"

	"bria/internal/screen"
	"bria/internal/settings"
	"bria/internal/telegram"
	"bria/internal/turnprocessing"
)

var (
	ErrInvalidConfiguration = errors.New("screen production configuration is invalid")
	ErrInvalidReceipt       = errors.New("screen production Telegram receipt is invalid")
)

type PhotoSender interface {
	SendPhoto(context.Context, telegram.SendPhotoRequest) (telegram.PhotoReceipt, error)
}

type Config struct {
	Store    *screen.Store
	Settings settings.Store
	Sender   PhotoSender
	ChatID   telegram.ChatID
}

type operation struct {
	media     screen.TelegramMedia
	handled   bool
	completed bool
}

type Composition struct {
	store    *screen.Store
	settings settings.Store
	sender   PhotoSender
	chatID   telegram.ChatID
	mu       sync.Mutex
	ops      map[string]operation
}

func Open(config Config) (*Composition, error) {
	if config.Store == nil || config.Settings == nil || config.Sender == nil || config.ChatID == 0 {
		return nil, ErrInvalidConfiguration
	}
	return &Composition{store: config.Store, settings: config.Settings, sender: config.Sender, chatID: config.ChatID, ops: make(map[string]operation)}, nil
}

func (composition *Composition) ObserveRuntimeEvent(ctx context.Context, observation turnprocessing.RuntimeEventObservation) error {
	if composition == nil || ctx == nil || !validObservation(observation) {
		return ErrInvalidConfiguration
	}
	composition.mu.Lock()
	defer composition.mu.Unlock()
	entry := composition.ops[observation.OperationID]
	if entry.completed {
		return nil
	}
	if !entry.handled {
		adapter, err := composition.store.Events(observation.SessionID)
		if err != nil {
			return err
		}
		if err := adapter.Handle(ctx, observation.Event); err != nil {
			return err
		}
		preferences, err := composition.settings.Load(ctx)
		if err != nil {
			return err
		}
		entry.handled = true
		if !preferences.ScreenEnabled {
			entry.completed = true
			composition.ops[observation.OperationID] = entry
			return nil
		}
		snapshot, err := composition.store.Snapshot(ctx, observation.SessionID)
		if err != nil {
			return err
		}
		entry.media = snapshot.TelegramMedia()
		composition.ops[observation.OperationID] = entry
	}
	receipt, err := composition.sender.SendPhoto(ctx, telegram.SendPhotoRequest{
		ChatID: composition.chatID, FileName: entry.media.FileName, ContentType: entry.media.ContentType, Content: append([]byte(nil), entry.media.Content...),
	})
	if err != nil {
		return err
	}
	if receipt.ChatID != composition.chatID || receipt.MessageID <= 0 {
		return ErrInvalidReceipt
	}
	entry.completed = true
	composition.ops[observation.OperationID] = entry
	return nil
}

func validObservation(observation turnprocessing.RuntimeEventObservation) bool {
	return strings.TrimSpace(observation.OperationID) == observation.OperationID && observation.OperationID != "" &&
		observation.SessionID != "" && strings.TrimSpace(observation.MessageID) == observation.MessageID && observation.MessageID != "" &&
		observation.EventIndex > 0
}

var _ turnprocessing.RuntimeEventObserver = (*Composition)(nil)
