package telegrambot

import (
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestScreenFingerprintCoversVisibleContentOnly(t *testing.T) {
	base := richTestScreen(testPanePNG(t))
	baseline := screenFingerprint(base)
	var variants = map[string]telegramui.Screen{}

	changedText := base
	changedText.Text += " changed"
	variants["text"] = changedText

	changedGrid := base
	changedGrid.Grid = telegramui.Grid{telegramui.Row{{
		Label: "Sessions", Callback: telegramui.Callback{Action: telegramui.ActionSessions},
	}}}
	variants["grid"] = changedGrid

	changedPane := base
	changedPane.Pane = &telegramui.PaneImage{
		PNG: base.Pane.PNG, Hash: "pane-v2", AnchorOffset: base.Pane.AnchorOffset,
	}
	variants["pane"] = changedPane

	changedAnchor := base
	changedAnchor.Pane = &telegramui.PaneImage{
		PNG: base.Pane.PNG, Hash: base.Pane.Hash, AnchorOffset: base.Pane.AnchorOffset + 1,
	}
	variants["pane anchor"] = changedAnchor

	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			if got := screenFingerprint(variant); got == baseline {
				t.Fatalf("visible %s change kept fingerprint %q", name, got)
			}
		})
	}

	metadataOnly := base
	metadataOnly.Name = telegramui.ScreenStatus
	metadataOnly.Checkpoint = &telegramui.SessionCheckpoint{
		NodeID: "node", SessionID: "session", Revision: 1, EventAt: time.Unix(1, 0),
	}
	if got := screenFingerprint(metadataOnly); got != baseline {
		t.Fatalf("handler-only metadata changed fingerprint: %q want %q", got, baseline)
	}
}
