package main

import (
	"context"
	"fmt"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/telegrambot"
)

func activateTelegramLeader(
	ctx context.Context,
	client *telegrambot.Client,
	service *application.Service,
) error {
	identity, err := client.GetMe(ctx)
	if err != nil {
		return fmt.Errorf("resolve Telegram bot identity: %w", err)
	}
	if err := service.BindTelegramBot(ctx, identity.ID); err != nil {
		return fmt.Errorf("bind Telegram bot identity: %w", err)
	}
	if err := client.SetMyCommands(ctx, []telegrambot.BotCommand{{
		Command: "menu", Description: "Open menu",
	}}, ""); err != nil {
		return fmt.Errorf("publish Telegram commands: %w", err)
	}
	if err := client.SetMyCommands(ctx, []telegrambot.BotCommand{{
		Command: "menu", Description: "Открыть меню",
	}}, "ru"); err != nil {
		return fmt.Errorf("publish Russian Telegram commands: %w", err)
	}
	return nil
}
