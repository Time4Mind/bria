package callbacktoken_test

import (
	"testing"

	"bria/internal/callbacktoken"
)

func TestScreenImageProfileUsesDistinctGlobalWireID(t *testing.T) {
	if callbacktoken.ActionSettingsScreenImageProfile != 95 {
		t.Fatalf("screen image profile wire ID = %d, want 95", callbacktoken.ActionSettingsScreenImageProfile)
	}
	testSettingsWire(t, callbacktoken.ActionSettingsScreenImageProfile)
}
