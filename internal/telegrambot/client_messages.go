package telegrambot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func (c *Client) SendMessage(ctx context.Context, request MessageRequest) (Message, error) {
	if request.ChatID <= 0 || !validMessageText(request.Text) || !validParseMode(request.ParseMode) {
		return Message{}, errors.New("invalid outgoing message")
	}
	keyboard, err := convertGrid(request.Grid)
	if err != nil {
		return Message{}, err
	}
	var result apiMessageResult
	err = c.call(ctx, "sendMessage", sendMessagePayload{
		ChatID: request.ChatID, Text: request.Text, ParseMode: string(request.ParseMode),
		LinkPreview: linkPreviewOptions{IsDisabled: true}, ReplyMarkup: keyboard,
	}, &result, c.requestTimeout)
	if err != nil {
		return Message{}, err
	}
	return validateMessageResult(result, request.ChatID)
}

func (c *Client) EditMessage(ctx context.Context, request EditMessageRequest) (Message, error) {
	if request.ChatID <= 0 || request.MessageID <= 0 || !validMessageText(request.Text) ||
		!validParseMode(request.ParseMode) {
		return Message{}, errors.New("invalid outgoing message edit")
	}
	keyboard, err := convertGrid(request.Grid)
	if err != nil {
		return Message{}, err
	}
	var result apiMessageResult
	err = c.call(ctx, "editMessageText", editMessagePayload{
		ChatID: request.ChatID, MessageID: request.MessageID,
		Text: request.Text, ParseMode: string(request.ParseMode),
		LinkPreview: linkPreviewOptions{IsDisabled: true}, ReplyMarkup: keyboard,
	}, &result, c.requestTimeout)
	if isUnchangedMessageError(err) {
		return Message{ChatID: request.ChatID, MessageID: request.MessageID}, nil
	}
	if err != nil {
		return Message{}, err
	}
	return validateMessageResult(result, request.ChatID)
}

func (c *Client) DeleteMessage(ctx context.Context, message Message) error {
	if message.ChatID <= 0 || message.MessageID <= 0 {
		return errors.New("invalid outgoing message deletion")
	}
	var deleted bool
	if err := c.call(ctx, "deleteMessage", deleteMessagePayload{
		ChatID: message.ChatID, MessageID: message.MessageID,
	}, &deleted, c.requestTimeout); err != nil {
		return err
	}
	if !deleted {
		return errors.New("Telegram did not delete the message")
	}
	return nil
}

func (c *Client) ClearKeyboard(ctx context.Context, message Message) error {
	if message.ChatID <= 0 || message.MessageID <= 0 {
		return errors.New("invalid message keyboard target")
	}
	var result json.RawMessage
	err := c.call(ctx, "editMessageReplyMarkup", editReplyMarkupPayload{
		ChatID: message.ChatID, MessageID: message.MessageID,
		ReplyMarkup: inlineKeyboardMarkup{InlineKeyboard: [][]inlineKeyboardButton{}},
	}, &result, c.requestTimeout)
	if isUnchangedMessageError(err) {
		return nil
	}
	return err
}

func isUnchangedMessageError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest &&
		strings.Contains(strings.ToLower(apiErr.Description), "message is not modified")
}

func validMessageText(text string) bool {
	return strings.TrimSpace(text) != "" && len([]byte(text)) <= MaxMessageTextBytes
}

func validParseMode(mode telegramui.ParseMode) bool {
	return mode == "" || mode == telegramui.ParseModeHTML || mode == telegramui.ParseModeMarkdownV2
}

func validateMessageResult(result apiMessageResult, expectedChatID int64) (Message, error) {
	if result.MessageID <= 0 || result.Chat.ID != expectedChatID {
		return Message{}, errors.New("telegram returned an invalid message identity")
	}
	return Message{ChatID: result.Chat.ID, MessageID: result.MessageID}, nil
}
