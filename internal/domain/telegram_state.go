package domain

import "fmt"

type TelegramResponseCard struct {
	ChatID          int64      `json:"chat_id"`
	MessageID       int64      `json:"message_id"`
	Rich            bool       `json:"rich,omitempty"`
	RichMediaFileID string     `json:"rich_media_file_id,omitempty"`
	PaneHash        string     `json:"pane_hash,omitempty"`
	Session         SessionRef `json:"session,omitempty"`
	SessionRevision uint64     `json:"session_revision,omitempty"`
}

// BindTelegramBot associates transport state with one Telegram bot. A cursor
// and response-card message IDs are meaningful only for the bot that created
// them, so changing bot identity must discard that transport-only state.
func (s *State) BindTelegramBot(botID int64) error {
	if botID <= 0 {
		return fmt.Errorf("Telegram bot id must be positive")
	}
	if s.TelegramBotID == botID {
		return nil
	}
	s.TelegramBotID = botID
	s.TelegramNextUpdateID = 0
	s.TelegramResponseCards = make(map[UserID]TelegramResponseCard)
	return nil
}

func (s *State) AdvanceTelegramCursor(nextUpdateID int64) error {
	if nextUpdateID < 0 {
		return fmt.Errorf("Telegram update cursor must not be negative")
	}
	if nextUpdateID < s.TelegramNextUpdateID {
		return ErrInvalidState
	}
	s.TelegramNextUpdateID = nextUpdateID
	return nil
}

func (s *State) RecordTelegramResponseCard(userID UserID, card TelegramResponseCard) error {
	if _, ok := s.Users[userID]; !ok {
		return ErrNotFound
	}
	if card.ChatID <= 0 || card.MessageID <= 0 {
		return fmt.Errorf("Telegram response card identity must be positive")
	}
	if card.ChatID != int64(userID) {
		return ErrAccessDenied
	}
	if len(card.RichMediaFileID) > 1024 || len(card.PaneHash) > 128 {
		return fmt.Errorf("Telegram response card transport metadata is invalid")
	}
	if card.Session != (SessionRef{}) {
		if err := card.Session.Validate(); err != nil || card.SessionRevision == 0 ||
			!s.CanViewSession(userID, card.Session) {
			return ErrAccessDenied
		}
	} else if card.SessionRevision != 0 {
		return fmt.Errorf("Telegram response card session revision has no session")
	}
	if s.TelegramResponseCards == nil {
		s.TelegramResponseCards = make(map[UserID]TelegramResponseCard)
	}
	s.TelegramResponseCards[userID] = card
	return nil
}
