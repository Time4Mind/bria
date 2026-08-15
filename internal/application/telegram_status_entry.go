package application

import "github.com/Time4Mind/bria/internal/telegramui"

func (p *TelegramProjector) Status(actor Principal) (telegramui.Screen, error) {
	return p.StatusMode(actor, telegramui.StatusChoose)
}

func (p *TelegramProjector) StatusMode(
	actor Principal,
	mode telegramui.StatusMode,
) (telegramui.Screen, error) {
	return p.StatusModeWithReturn(actor, mode, telegramui.ActionMenu, "")
}
