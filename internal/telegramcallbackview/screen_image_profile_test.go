package telegramcallbackview_test

import (
	"testing"

	"bria/internal/callbacktoken"
	"bria/internal/telegramui"
)

func TestScreenImageProfileRejectsUnexpectedTarget(t *testing.T) {
	testGlobalToggle(t, telegramui.ActionSettingsScreenImageProfile, "Качество скрина", callbacktoken.ActionSettingsScreenImageProfile)
}

func TestScreenControlsUseCardTerminology(t *testing.T) {
	testGlobalToggle(t, telegramui.ActionSettingsScreen, "Скрин", callbacktoken.ActionSettingsScreen)
	testGlobalToggle(t, telegramui.ActionSettingsScreenCaptureLimit, "Размер захвата скрина", callbacktoken.ActionSettingsScreenCaptureLimit)
}
