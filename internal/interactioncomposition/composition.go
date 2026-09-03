// Package interactioncomposition connects durable provider interaction state
// to the signed Telegram flow without introducing a construction cycle.
package interactioncomposition

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"bria/internal/interactionflow"
	"bria/internal/telegram"
	"bria/internal/telegrambridge"
	"bria/internal/telegramflow"
	"bria/internal/telegrampipeline"
)

var ErrInvalidOptions = errors.New("provider interaction composition is unavailable")

type TelegramMessageDeleter interface {
	DeleteMessage(context.Context, telegram.DeleteMessageRequest) error
}

type Options struct {
	StorePath      string
	ConversationID int64
	OwnerActorID   int64
	Telegram       TelegramMessageDeleter
}

type Composition struct {
	flow  *interactionflow.Flow
	relay *interactionflow.DeliveryRelay
}

func Open(options Options) (*Composition, error) {
	if options.ConversationID <= 0 || options.OwnerActorID <= 0 ||
		strings.TrimSpace(options.StorePath) == "" || !filepath.IsAbs(options.StorePath) || nilInterface(options.Telegram) {
		return nil, ErrInvalidOptions
	}
	store, err := interactionflow.OpenFileStore(options.StorePath)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	relay := interactionflow.NewDeliveryRelay()
	flow, err := interactionflow.New(store, relay, interactionflow.Options{
		ConversationID: options.ConversationID,
		OwnerActorID:   options.OwnerActorID,
		SecretDeleter:  telegramDeleter{client: options.Telegram},
	})
	if err != nil {
		return nil, ErrInvalidOptions
	}
	return &Composition{flow: flow, relay: relay}, nil
}

func (composition *Composition) Flow() *interactionflow.Flow {
	if composition == nil {
		return nil
	}
	return composition.flow
}

func (composition *Composition) Bind(sender interactionflow.PreparedSender, presenter *telegrambridge.Presenter) error {
	if composition == nil || composition.relay == nil {
		return ErrInvalidOptions
	}
	delivery, err := interactionflow.NewTelegramDeliverySender(sender, presenter)
	if err != nil {
		return err
	}
	return composition.relay.Bind(delivery)
}

type CallbackRouter struct {
	normal      telegramflow.CallbackExecutor
	interaction telegramflow.CallbackExecutor
}

func NewCallbackRouter(normal, interaction telegramflow.CallbackExecutor) (*CallbackRouter, error) {
	if nilInterface(normal) || nilInterface(interaction) {
		return nil, ErrInvalidOptions
	}
	return &CallbackRouter{normal: normal, interaction: interaction}, nil
}

func (router *CallbackRouter) HandleCallback(ctx context.Context, plan telegrampipeline.CallbackPlan) (telegramflow.CallbackResult, error) {
	if router == nil || router.normal == nil || router.interaction == nil {
		return telegramflow.CallbackResult{}, ErrInvalidOptions
	}
	if plan.Interaction != nil {
		return router.interaction.HandleCallback(ctx, plan)
	}
	return router.normal.HandleCallback(ctx, plan)
}

type telegramDeleter struct{ client TelegramMessageDeleter }

func (deleter telegramDeleter) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	err := deleter.client.DeleteMessage(ctx, telegram.DeleteMessageRequest{
		ChatID: telegram.ChatID(chatID), MessageID: telegram.MessageID(messageID),
	})
	if err == nil {
		return nil
	}
	// deleteMessage is idempotent for the exact source binding. Accept only
	// Telegram's definitive already-absent response; timeouts, transport errors,
	// permissions, and all other 400s remain unknown and fail closed.
	var apiError *telegram.APIError
	if errors.As(err, &apiError) && apiError.Method == "deleteMessage" && apiError.ErrorCode == 400 &&
		apiError.Description == "Bad Request: message to delete not found" {
		return nil
	}
	return err
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ telegramflow.CallbackExecutor = (*CallbackRouter)(nil)
