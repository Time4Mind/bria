package telegramview

import "github.com/Time4Mind/bria/internal/telegramui"

func (p *Projector) Status(actor Principal) (telegramui.Screen, error) {
	return p.StatusMode(actor, telegramui.StatusChoose)
}

func (p *Projector) StatusMode(
	actor Principal,
	mode telegramui.StatusMode,
) (telegramui.Screen, error) {
	return p.StatusModeWithReturn(actor, mode, telegramui.ActionMenu, "")
}
