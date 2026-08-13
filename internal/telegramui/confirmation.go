package telegramui

import "github.com/Time4Mind/bria/internal/i18n"

type ConfirmationInput struct {
	Copy          i18n.Localizer
	Text          string
	ConfirmLabel  string
	ConfirmAction Action
	ConfirmToken  OpaqueToken
	CancelToken   OpaqueToken
}

func RenderConfirmation(input ConfirmationInput) Screen {
	return Screen{
		Name: ScreenSessionCard,
		Text: input.Text,
		Grid: Grid{Row{
			button(input.ConfirmLabel, input.ConfirmAction, input.ConfirmToken),
			button(input.Copy.Text(i18n.ButtonCancel), ActionCancelControl, input.CancelToken),
		}},
	}
}
