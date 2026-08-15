package telegrambot

import (
	"context"
	"errors"
	"strings"
)

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type setMyCommandsPayload struct {
	Commands     []BotCommand `json:"commands"`
	LanguageCode string       `json:"language_code,omitempty"`
}

// SetMyCommands publishes the native Telegram slash-command menu.
func (c *Client) SetMyCommands(
	ctx context.Context,
	commands []BotCommand,
	languageCode string,
) error {
	if len(commands) == 0 || len(commands) > 100 {
		return errors.New("Telegram command list must contain 1 to 100 commands")
	}
	if len(languageCode) > 8 || strings.ContainsAny(languageCode, "\r\n\t ") {
		return errors.New("invalid Telegram command language")
	}
	for _, command := range commands {
		if !validCommandName(command.Command) || strings.TrimSpace(command.Description) == "" ||
			len([]byte(command.Description)) > 256 || strings.ContainsAny(command.Description, "\r\n") {
			return errors.New("invalid Telegram bot command")
		}
	}
	return c.call(ctx, "setMyCommands", setMyCommandsPayload{
		Commands: commands, LanguageCode: languageCode,
	}, nil, c.requestTimeout)
}

func validCommandName(command string) bool {
	if command == "" || len(command) > 32 {
		return false
	}
	for _, char := range command {
		if !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}
	return true
}
