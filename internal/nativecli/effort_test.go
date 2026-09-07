package nativecli

import "testing"

func TestNativeReasoningPickerIsInteractive(t *testing.T) {
	state := ParseScreen("Select Reasoning Level for gpt-test\n› 2. Medium (default) (current)\n  3. High\nPress enter to confirm or esc to go back")
	if !state.Interactive || state.Ready {
		t.Fatalf("reasoning picker must consume native input, not a model prompt: %+v", state)
	}
}
