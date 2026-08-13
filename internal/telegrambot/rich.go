package telegrambot

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func (c *Client) sendRichScreen(
	ctx context.Context,
	chatID int64,
	screen telegramui.Screen,
) (Message, error) {
	if screen.Pane == nil && !screen.RichMarkdown {
		return Message{}, errors.New("screen has no rich content")
	}
	keyboard, err := convertGrid(screen.Grid)
	if err != nil {
		return Message{}, err
	}
	rich, pngBytes, err := richScreenMessage(screen, Message{})
	if err != nil {
		return Message{}, err
	}
	payload := sendRichMessagePayload{
		ChatID: chatID, RichMessage: rich, DisableNotification: true, ReplyMarkup: keyboard,
	}
	raw, err := c.callRich(ctx, "sendRichMessage", payload, chatID, 0, rich, keyboard, pngBytes)
	if err != nil {
		return Message{}, err
	}
	message, err := parseRichResult(raw, Message{ChatID: chatID})
	if err != nil {
		return Message{}, err
	}
	if screen.Pane != nil {
		message.PaneHash = screen.Pane.Hash
	}
	return message, nil
}

func (c *Client) editRichScreen(
	ctx context.Context,
	previous Message,
	screen telegramui.Screen,
) (Message, error) {
	// A rich carrier must remain rich even when its next projection has no
	// technical block or screenshot (for example while confirming Close).
	keyboard, err := convertGrid(screen.Grid)
	if err != nil {
		return Message{}, err
	}
	rich, pngBytes, err := richScreenMessage(screen, previous)
	if err != nil {
		return Message{}, err
	}
	payload := editRichMessagePayload{
		ChatID: previous.ChatID, MessageID: previous.MessageID,
		RichMessage: rich, ReplyMarkup: keyboard,
	}
	raw, err := c.callRich(
		ctx, "editMessageText", payload, previous.ChatID, previous.MessageID,
		rich, keyboard, pngBytes,
	)
	if isUnchangedMessageError(err) {
		return previous, nil
	}
	if err != nil {
		return Message{}, err
	}
	message, err := parseRichResult(raw, previous)
	if err != nil {
		return Message{}, err
	}
	if len(pngBytes) > 0 {
		// A lightweight Bot API proxy may return only true for an upload edit.
		// Never retain the prior file ID in that case: reusing it would restore
		// the old screenshot on the next text-only rich edit.
		message.RichMediaFileID = extractRichPhotoFileID(raw)
	} else if screen.Pane == nil {
		message.RichMediaFileID = ""
	}
	if screen.Pane != nil {
		message.PaneHash = screen.Pane.Hash
	} else {
		message.PaneHash = ""
	}
	return message, nil
}

func richScreenMessage(
	screen telegramui.Screen,
	previous Message,
) (richMessage, []byte, error) {
	if screen.Pane == nil {
		rich, err := buildRichTextMessage(screen.Text)
		return rich, nil, err
	}
	reference, pngBytes, err := paneReference(*screen.Pane, previous)
	if err != nil {
		return richMessage{}, nil, err
	}
	rich, err := buildRichMessage(screen.Text, *screen.Pane, reference)
	return rich, pngBytes, err
}

func (c *Client) callRich(
	ctx context.Context,
	method string,
	payload any,
	chatID int64,
	messageID int64,
	rich richMessage,
	keyboard inlineKeyboardMarkup,
	pngBytes []byte,
) (json.RawMessage, error) {
	if len(pngBytes) > 0 {
		return c.callRichMultipart(
			ctx, method, chatID, messageID, rich, keyboard, pngBytes, c.richRequestTimeout,
		)
	}
	var raw json.RawMessage
	if err := c.call(ctx, method, payload, &raw, c.richRequestTimeout); err != nil {
		return nil, err
	}
	return raw, nil
}
