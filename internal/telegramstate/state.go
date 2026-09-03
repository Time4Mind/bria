// Package telegramstate owns the durable, user-visible Telegram UI state.
// It deliberately contains no Telegram transport or coordinator policy.
package telegramstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"bria/internal/domain"
)

const (
	FormatVersion = 1
	maxPages      = 512
	maxAnchor     = 1024
)

// Carrier identifies the Telegram message currently carrying a session card.
// Both values are zero before the first successful send, or both are positive.
type Carrier struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

// Page is the durable reading position for one session's history.
type Page struct {
	Current      int    `json:"current"`
	Total        int    `json:"total"`
	Anchor       string `json:"anchor,omitempty"`
	FollowLatest bool   `json:"follow_latest"`
}

// Card is the durable presentation state for one logical session.
type Card struct {
	SessionID       domain.SessionID `json:"session_id"`
	Carrier         Carrier          `json:"carrier"`
	Page            Page             `json:"page"`
	OptionsExpanded bool             `json:"options_expanded"`
	History         []string         `json:"history,omitempty"`
}

// State is the complete Telegram UI state for the configured owner chat.
type State struct {
	Version       int                       `json:"version"`
	ActiveSession domain.SessionID          `json:"active_session,omitempty"`
	ScreenEnabled bool                      `json:"screen_enabled"`
	Cards         map[domain.SessionID]Card `json:"cards"`
}

// New returns an empty state with Screen disabled by default.
func New() State { return State{Version: FormatVersion, Cards: make(map[domain.SessionID]Card)} }

func (s State) Clone() State {
	clone := New()
	clone.Version, clone.ActiveSession, clone.ScreenEnabled = s.Version, s.ActiveSession, s.ScreenEnabled
	for id, card := range s.Cards {
		card.History = append([]string(nil), card.History...)
		clone.Cards[id] = card
	}
	return clone
}

func (s State) Validate() error {
	if s.Version != FormatVersion {
		return fmt.Errorf("unsupported Telegram UI state version %d", s.Version)
	}
	if s.Cards == nil {
		return errors.New("cards map is required")
	}
	for id, card := range s.Cards {
		if strings.TrimSpace(string(id)) == "" || card.SessionID != id {
			return fmt.Errorf("card session identity is invalid")
		}
		if err := card.Page.validate(); err != nil {
			return fmt.Errorf("card %q page: %w", id, err)
		}
		if err := card.Carrier.validate(); err != nil {
			return fmt.Errorf("card %q carrier: %w", id, err)
		}
		if len(card.History) > 512 {
			return fmt.Errorf("card %q history is too long", id)
		}
		for _, item := range card.History {
			if item == "" || !utf8.ValidString(item) || len(item) > 16384 {
				return fmt.Errorf("card %q history item is invalid", id)
			}
		}
	}
	if s.ActiveSession != "" {
		if _, ok := s.Cards[s.ActiveSession]; !ok {
			return fmt.Errorf("active session %q has no card", s.ActiveSession)
		}
	}
	return nil
}

func (p Page) validate() error {
	if p.Current < 1 || p.Total < 1 || p.Current > p.Total || p.Total > maxPages {
		return fmt.Errorf("page must be within 1..%d", maxPages)
	}
	if len(p.Anchor) > maxAnchor || !utf8.ValidString(p.Anchor) {
		return fmt.Errorf("anchor is invalid")
	}
	return nil
}

func (c Carrier) validate() error {
	if c.ChatID == 0 && c.MessageID == 0 {
		return nil
	}
	if c.ChatID <= 0 || c.MessageID <= 0 {
		return errors.New("chat and message IDs must be both zero or positive")
	}
	return nil
}

// Card returns a copy of a session card.
func (s State) Card(id domain.SessionID) (Card, bool) { card, ok := s.Cards[id]; return card, ok }

// SetCard validates and replaces one card, returning an error without mutation.
func (s *State) SetCard(card Card) error {
	if s == nil {
		return errors.New("nil UI state")
	}
	if strings.TrimSpace(string(card.SessionID)) == "" {
		return errors.New("card session id is required")
	}
	if err := (State{Version: s.Version, Cards: map[domain.SessionID]Card{card.SessionID: card}}).Validate(); err != nil {
		return err
	}
	if s.Cards == nil {
		s.Cards = make(map[domain.SessionID]Card)
	}
	s.Cards[card.SessionID] = card
	return nil
}

// Store is the persistence seam used by the coordinator/UI layer.
type Store interface {
	Load(context.Context) (State, error)
	Update(context.Context, func(*State) error) error
}

type MemoryStore struct {
	mu    sync.Mutex
	state State
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{state: New()} }
func (m *MemoryStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.Clone(), nil
}
func (m *MemoryStore) Update(ctx context.Context, fn func(*State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("update function is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state.Clone()
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("validate UI state: %w", err)
	}
	m.state = next
	return nil
}

type FileStore struct {
	mu    sync.Mutex
	path  string
	state State
}

func OpenFileStore(path string) (*FileStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("UI state path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := &FileStore{path: abs, state: New()}
	data, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("decode UI state: %w", err)
	}
	if err := s.state.Validate(); err != nil {
		return nil, fmt.Errorf("validate UI state: %w", err)
	}
	return s, nil
}
func (f *FileStore) Load(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state.Clone(), nil
}
func (f *FileStore) Update(ctx context.Context, fn func(*State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("update function is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	next := f.state.Clone()
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("validate UI state: %w", err)
	}
	if err := writeAtomic(f.path, next); err != nil {
		return err
	}
	f.state = next
	return nil
}
func writeAtomic(path string, s State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".telegramstate-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err == nil {
		err = d.Sync()
		_ = d.Close()
	}
	return err
}
