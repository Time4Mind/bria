package telegramcallbackview_test

import (
	"testing"

	"bria/internal/callbacktoken"
	"bria/internal/telegramui"
)

func TestScreenImageProfileRejectsUnexpectedTarget(t *testing.T) {
	testGlobalToggle(t, telegramui.ActionSettingsScreenImageProfile, "Изображение Screen", callbacktoken.ActionSettingsScreenImageProfile)
}
