// Package telegramui defines Bria's Telegram-neutral screen model.
//
// It deliberately contains no Telegram SDK types. Adapters render Screen values
// into the concrete transport and encode semantic callbacks at the boundary.
package telegramui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type ScreenName string
type ParseMode string

const (
	ParseModeHTML       ParseMode = "HTML"
	ParseModeMarkdownV2 ParseMode = "MarkdownV2"
)

const (
	ScreenMenu        ScreenName = "menu"
	ScreenNodes       ScreenName = "nodes"
	ScreenSessions    ScreenName = "sessions"
	ScreenArchives    ScreenName = "archives"
	ScreenSessionCard ScreenName = "session_card"
	ScreenSettings    ScreenName = "settings"
	ScreenStatus      ScreenName = "status"
)

type Button struct {
	Label    string
	Callback Callback
}

type Row []Button
type Grid []Row

// PaneImage is optional rich-card media. PNG is used for a fresh upload;
// FileID reuses a Telegram-side upload. AnchorOffset is a UTF-8 byte offset in
// Screen.Text; zero appends the image after the text.
type PaneImage struct {
	PNG          []byte
	Hash         string
	FileID       string
	AnchorOffset int
}

// SessionCheckpoint identifies the exact replicated session state rendered
// into a card. It is handler metadata and is never sent to Telegram.
type SessionCheckpoint struct {
	NodeID    string
	SessionID string
	Revision  uint64
	EventAt   time.Time
}

type Screen struct {
	Name         ScreenName
	Text         string
	ParseMode    ParseMode
	RichMarkdown bool
	Grid         Grid
	Pane         *PaneImage
	Checkpoint   *SessionCheckpoint
}

// Validate checks constraints that the eventual Telegram adapter must honor.
func (s Screen) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("screen name is required")
	}
	if strings.TrimSpace(s.Text) == "" {
		return fmt.Errorf("screen text is required")
	}
	if s.ParseMode != "" && s.ParseMode != ParseModeHTML && s.ParseMode != ParseModeMarkdownV2 {
		return fmt.Errorf("unsupported parse mode: %q", s.ParseMode)
	}
	if s.RichMarkdown && s.ParseMode != "" {
		return fmt.Errorf("rich Markdown and parse mode cannot be combined")
	}
	if s.Pane != nil {
		if len(s.Pane.PNG) == 0 && strings.TrimSpace(s.Pane.FileID) == "" {
			return fmt.Errorf("pane image has neither PNG nor Telegram file ID")
		}
		if s.Pane.AnchorOffset < 0 || s.Pane.AnchorOffset > len(s.Text) ||
			!utf8.ValidString(s.Text[:s.Pane.AnchorOffset]) {
			return fmt.Errorf("pane image anchor is not a UTF-8 text boundary")
		}
	}
	if s.Checkpoint != nil && (strings.TrimSpace(s.Checkpoint.NodeID) == "" ||
		strings.TrimSpace(s.Checkpoint.SessionID) == "" || s.Checkpoint.Revision == 0 ||
		s.Checkpoint.EventAt.IsZero()) {
		return fmt.Errorf("session checkpoint is incomplete")
	}
	for rowIndex, row := range s.Grid {
		if len(row) == 0 {
			return fmt.Errorf("grid row %d is empty", rowIndex)
		}
		for buttonIndex, button := range row {
			if strings.TrimSpace(button.Label) == "" {
				return fmt.Errorf("button %d:%d has no label", rowIndex, buttonIndex)
			}
			if _, err := button.Callback.Encode(); err != nil {
				return fmt.Errorf("button %d:%d: %w", rowIndex, buttonIndex, err)
			}
		}
	}
	return nil
}

// CanonicalGrid is a stable, SDK-independent representation for golden tests.
func CanonicalGrid(grid Grid) string {
	var out strings.Builder
	for rowIndex, row := range grid {
		if rowIndex > 0 {
			out.WriteByte('\n')
		}
		for buttonIndex, button := range row {
			if buttonIndex > 0 {
				out.WriteString(" | ")
			}
			out.WriteString("[")
			out.WriteString(button.Label)
			out.WriteString(" -> ")
			out.WriteString(string(button.Callback.Action))
			if button.Callback.Token != "" {
				out.WriteString("@")
				out.WriteString(string(button.Callback.Token))
			}
			out.WriteString("]")
		}
	}
	return out.String()
}
