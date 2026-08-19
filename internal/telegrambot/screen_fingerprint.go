package telegrambot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/Time4Mind/bria/internal/telegramui"
)

type fingerprintPane struct {
	Hash         string `json:"hash,omitempty"`
	FileID       string `json:"file_id,omitempty"`
	PNGHash      string `json:"png_hash,omitempty"`
	AnchorOffset int    `json:"anchor_offset,omitempty"`
}

type fingerprintScreen struct {
	Text             string               `json:"text"`
	ParseMode        telegramui.ParseMode `json:"parse_mode,omitempty"`
	RichMarkdown     bool                 `json:"rich_markdown,omitempty"`
	Grid             telegramui.Grid      `json:"grid,omitempty"`
	Pane             *fingerprintPane     `json:"pane,omitempty"`
	PaneAnchorOffset int                  `json:"pane_anchor_offset,omitempty"`
}

// screenFingerprint identifies only the Telegram-visible projection. Handler
// metadata such as the screen name and session checkpoint must not turn an
// otherwise identical card into an outbound edit.
func screenFingerprint(screen telegramui.Screen) string {
	payload := fingerprintScreen{
		Text: screen.Text, ParseMode: screen.ParseMode, RichMarkdown: screen.RichMarkdown,
		Grid: screen.Grid, PaneAnchorOffset: screen.PaneAnchorOffset,
	}
	if screen.Pane != nil {
		pane := &fingerprintPane{Hash: screen.Pane.Hash, AnchorOffset: screen.Pane.AnchorOffset}
		if pane.Hash == "" {
			pane.FileID = screen.Pane.FileID
			if len(screen.Pane.PNG) > 0 {
				sum := sha256.Sum256(screen.Pane.PNG)
				pane.PNGHash = hex.EncodeToString(sum[:])
			}
		}
		payload.Pane = pane
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err) // fingerprintScreen contains only JSON-safe transport values.
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// ScreenFingerprint exposes the stable Telegram-visible identity to delivery
// coordinators that must suppress duplicate replacement messages across a
// process restart.
func ScreenFingerprint(screen telegramui.Screen) string {
	return screenFingerprint(screen)
}

func stampScreen(message Message, screen telegramui.Screen) Message {
	message.ScreenHash = screenFingerprint(screen)
	return message
}
