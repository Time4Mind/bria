package telegrambot

import (
	"context"
	"fmt"
	"log"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func (c *Client) SendScreen(
	ctx context.Context,
	chatID int64,
	screen telegramui.Screen,
) (Message, error) {
	if err := screen.Validate(); err != nil {
		return Message{}, err
	}
	if screen.Pane != nil || screen.RichMarkdown {
		message, richErr := c.sendRichScreen(ctx, chatID, screen)
		if richErr == nil {
			return message, nil
		}
		// A flood wait applies to the chat, not just to the rich transport.
		// Falling back immediately doubles the rejected traffic and can turn a
		// short cooldown into an hours-long Telegram ban.
		if _, limited := FloodWait(richErr); limited {
			return Message{}, richErr
		}
		log.Printf("bria telegram: rich send failed; using expandable fallback: %v", richErr)
		fallbackText := richFallbackMarkdownV2(screen)
		fallback, fallbackErr := c.SendMessage(ctx, MessageRequest{
			ChatID: chatID, Text: fallbackText, ParseMode: telegramui.ParseModeMarkdownV2,
			Grid: screen.Grid,
		})
		if fallbackErr != nil {
			return Message{}, fmt.Errorf("rich screen failed: %v; text fallback: %w", richErr, fallbackErr)
		}
		return fallback, nil
	}
	return c.SendMessage(ctx, MessageRequest{
		ChatID: chatID, Text: screen.Text, ParseMode: screen.ParseMode, Grid: screen.Grid,
	})
}

func (c *Client) EditScreen(
	ctx context.Context,
	message Message,
	screen telegramui.Screen,
) (Message, error) {
	if err := screen.Validate(); err != nil {
		return Message{}, err
	}
	// Bot API rich messages are a distinct carrier. Editing one through the
	// legacy text endpoint can leave clients with an unsupported empty message,
	// while editing a legacy message through the rich endpoint silently changes
	// its carrier type. Keep the transport stable for the lifetime of a card.
	if message.Rich {
		updated, richErr := c.editRichScreen(ctx, message, screen)
		if richErr == nil {
			return updated, nil
		}
		return Message{}, richErr
	}
	if screen.Pane != nil || screen.RichMarkdown {
		fallbackText := richFallbackMarkdownV2(screen)
		return c.EditMessage(ctx, EditMessageRequest{
			ChatID: message.ChatID, MessageID: message.MessageID,
			Text: fallbackText, ParseMode: telegramui.ParseModeMarkdownV2,
			Grid: screen.Grid,
		})
	}
	return c.EditMessage(ctx, EditMessageRequest{
		ChatID: message.ChatID, MessageID: message.MessageID,
		Text: screen.Text, ParseMode: screen.ParseMode, Grid: screen.Grid,
	})
}
