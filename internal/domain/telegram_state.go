package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	telegramSessionViewMarker   = "view:session-page:"
	maxTelegramSessionViewPages = 512
)

// TelegramSessionView is navigation state for one user's view of one session.
// It deliberately lives outside Session: page position is presentation state,
// not shared CLI runtime state.
type TelegramSessionView struct {
	Page   int    `json:"page"`
	Pages  int    `json:"pages"`
	Anchor string `json:"anchor,omitempty"`
	Follow bool   `json:"follow"`
}

func (v TelegramSessionView) Validate() error {
	if v.Page < 1 || v.Pages < v.Page || v.Pages > maxTelegramSessionViewPages {
		return fmt.Errorf("Telegram session view page is invalid")
	}
	if len(v.Anchor) > 64 || strings.Contains(v.Anchor, ":") {
		return fmt.Errorf("Telegram session view anchor is invalid")
	}
	return nil
}

// EncodeTelegramSessionViewPaneHash carries typed view state through the
// existing Raft command understood by protocol-v1 nodes. Old replicas retain
// the bounded string unchanged; current replicas additionally project it into
// TelegramSessionViews. This avoids introducing an unknown command during a
// rolling update.
func EncodeTelegramSessionViewPaneHash(view TelegramSessionView, paneHash string) string {
	if view.Validate() != nil {
		return paneHash
	}
	mode := "p"
	if view.Follow {
		mode = "f"
	}
	marker := telegramSessionViewMarker + mode + ":" + strconv.Itoa(view.Page) + ":" +
		strconv.Itoa(view.Pages) + ":" + view.Anchor + ":"
	if len(marker)+len(paneHash) > 128 {
		return marker
	}
	return marker + paneHash
}

func DecodeTelegramSessionViewPaneHash(
	value string,
) (TelegramSessionView, string, bool) {
	payload, marked := strings.CutPrefix(value, telegramSessionViewMarker)
	if !marked {
		return TelegramSessionView{}, value, false
	}
	first, payload, foundFirst := strings.Cut(payload, ":")
	if !foundFirst {
		return TelegramSessionView{}, value, false
	}
	follow := false
	pageText := first
	if first == "f" || first == "p" {
		follow = first == "f"
		var foundPage bool
		pageText, payload, foundPage = strings.Cut(payload, ":")
		if !foundPage {
			return TelegramSessionView{}, value, false
		}
	}
	pagesText, remainder, foundPages := strings.Cut(payload, ":")
	if !foundPages {
		return TelegramSessionView{}, value, false
	}
	anchor, paneHash, foundAnchor := strings.Cut(remainder, ":")
	if !foundAnchor {
		// Explicit-mode markers briefly existed without an anchor. Preserve their
		// pane hint and fall back to the numeric position during rolling upgrade.
		if first == "f" || first == "p" {
			anchor = ""
			paneHash = remainder
		} else {
			anchor = ""
			paneHash = remainder
		}
	}
	page, pageErr := strconv.Atoi(pageText)
	pages, pagesErr := strconv.Atoi(pagesText)
	view := TelegramSessionView{Page: page, Pages: pages, Anchor: anchor, Follow: follow}
	if first != "f" && first != "p" {
		// Legacy markers had no explicit intent and can only preserve the old
		// approximation during an upgrade. Every new write uses f/p.
		view.Follow = page == pages
	}
	if pageErr != nil || pagesErr != nil || view.Validate() != nil {
		return TelegramSessionView{}, value, false
	}
	return view, paneHash, true
}

type TelegramResponseCard struct {
	ChatID          int64      `json:"chat_id"`
	MessageID       int64      `json:"message_id"`
	Rich            bool       `json:"rich,omitempty"`
	RichMediaFileID string     `json:"rich_media_file_id,omitempty"`
	PaneHash        string     `json:"pane_hash,omitempty"`
	ScreenHash      string     `json:"screen_hash,omitempty"`
	Session         SessionRef `json:"session,omitempty"`
	SessionRevision uint64     `json:"session_revision,omitempty"`
	SessionEventAt  time.Time  `json:"session_event_at,omitempty"`
	// RenderedFinalAt is the monotonic watermark of provider finals already
	// delivered through this Telegram carrier. The user may navigate the carrier
	// to a historical page without making that delivery undone.
	RenderedFinalAt time.Time `json:"rendered_final_at,omitempty"`
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
	if len(card.RichMediaFileID) > 1024 || len(card.PaneHash) > 128 || len(card.ScreenHash) > 64 {
		return fmt.Errorf("Telegram response card transport metadata is invalid")
	}
	if card.Session != (SessionRef{}) {
		if err := card.Session.Validate(); err != nil || card.SessionRevision == 0 ||
			!s.CanViewSession(userID, card.Session) {
			return ErrAccessDenied
		}
	} else if card.SessionRevision != 0 || !card.SessionEventAt.IsZero() ||
		!card.RenderedFinalAt.IsZero() {
		return fmt.Errorf("Telegram response card session checkpoint has no session")
	}
	if s.TelegramResponseCards == nil {
		s.TelegramResponseCards = make(map[UserID]TelegramResponseCard)
	}
	if view, _, ok := DecodeTelegramSessionViewPaneHash(card.PaneHash); ok &&
		card.Session != (SessionRef{}) {
		if s.TelegramSessionViews == nil {
			s.TelegramSessionViews = make(map[UserID]map[string]TelegramSessionView)
		}
		if s.TelegramSessionViews[userID] == nil {
			s.TelegramSessionViews[userID] = make(map[string]TelegramSessionView)
		}
		s.TelegramSessionViews[userID][card.Session.Key()] = view
	}
	if previous, ok := s.TelegramResponseCards[userID]; ok &&
		previous.ChatID == card.ChatID && previous.MessageID == card.MessageID &&
		previous.Session == card.Session && card.RenderedFinalAt.Before(previous.RenderedFinalAt) {
		// Delivery is monotonic for one Telegram carrier. Keep this invariant in
		// the replicated aggregate so stale retries and future adapters cannot
		// make an already delivered final look missing again.
		card.RenderedFinalAt = previous.RenderedFinalAt
	}
	s.TelegramResponseCards[userID] = card
	return nil
}

func (s *State) TelegramSessionView(
	userID UserID,
	ref SessionRef,
) (TelegramSessionView, bool) {
	if !s.CanViewSession(userID, ref) {
		return TelegramSessionView{}, false
	}
	view, ok := s.TelegramSessionViews[userID][ref.Key()]
	return view, ok && view.Validate() == nil
}
