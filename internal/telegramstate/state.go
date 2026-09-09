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
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"bria/internal/domain"
)

const (
	FormatVersion = 1
	maxPages      = 512
	maxAnchor     = 1024
	maxNodeID     = 256
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
	// EmptyCloseEligible is proof that this locally created session has never
	// received content. Missing legacy evidence is deliberately false.
	EmptyCloseEligible bool             `json:"empty_close_eligible,omitempty"`
	SessionID          domain.SessionID `json:"session_id"`
	Carrier            Carrier          `json:"carrier"`
	Page               Page             `json:"page"`
	OptionsExpanded    bool             `json:"options_expanded"`
	History            []string         `json:"history,omitempty"`
	// HistoryKeys is positionally aligned with History. Empty keys identify
	// append-only provider output; a prompt message ID lets its visible status
	// be replaced without duplicating the user's text.
	HistoryKeys []string `json:"history_keys,omitempty"`
	// HistoryTurnKeys associates provider output with its originating prompt so
	// projections can keep each answer directly after its request.
	HistoryTurnKeys []string `json:"history_turn_keys,omitempty"`
	// HistoryKinds is positionally aligned with History. "tool" identifies an
	// exact provider tool event; empty values are ordinary history. A missing
	// legacy slice means every retained item is ordinary.
	HistoryKinds []string `json:"history_kinds,omitempty"`
	// PendingFinalOperations fences routine edits until each exact final has
	// committed its new carrier. Missing legacy state does not invent custody.
	PendingFinalOperations []string `json:"pending_final_operations,omitempty"`
}

// PendingFinalsAfter returns an independent copy excluding only the exact
// nonempty confirmed operation. Empty or unknown operations preserve custody.
func (c Card) PendingFinalsAfter(operationID string) []string {
	pending := append([]string(nil), c.PendingFinalOperations...)
	if operationID == "" {
		return pending
	}
	pending = slices.DeleteFunc(pending, func(operation string) bool { return operation == operationID })
	if len(pending) == 0 {
		return nil // Match omitempty JSON and physical confirmed-card rereads.
	}
	return pending
}

// State is the complete Telegram UI state for the configured owner chat.
type State struct {
	Version        int                                      `json:"version"`
	SelectedNode   domain.ComputerID                        `json:"selected_node,omitempty"`
	ActiveSession  domain.SessionID                         `json:"active_session,omitempty"`
	ActiveSessions map[domain.ComputerID]domain.SessionID   `json:"active_sessions,omitempty"`
	RecentSessions map[domain.ComputerID][]domain.SessionID `json:"recent_sessions,omitempty"`
	ScreenEnabled  bool                                     `json:"screen_enabled"`
	Cards          map[domain.SessionID]Card                `json:"cards"`
}

// New returns an empty state with Screen disabled by default.
func New() State {
	return State{
		Version: FormatVersion, ActiveSessions: make(map[domain.ComputerID]domain.SessionID),
		RecentSessions: make(map[domain.ComputerID][]domain.SessionID), Cards: make(map[domain.SessionID]Card),
	}
}

func (s State) Clone() State {
	clone := New()
	clone.Version, clone.SelectedNode, clone.ActiveSession, clone.ScreenEnabled =
		s.Version, s.SelectedNode, s.ActiveSession, s.ScreenEnabled
	for nodeID, sessionID := range s.ActiveSessions {
		clone.ActiveSessions[nodeID] = sessionID
	}
	for nodeID, sessions := range s.RecentSessions {
		clone.RecentSessions[nodeID] = append([]domain.SessionID(nil), sessions...)
	}
	for id, card := range s.Cards {
		card.History = append([]string(nil), card.History...)
		card.HistoryKeys = append([]string(nil), card.HistoryKeys...)
		card.HistoryTurnKeys = append([]string(nil), card.HistoryTurnKeys...)
		card.HistoryKinds = append([]string(nil), card.HistoryKinds...)
		card.PendingFinalOperations = card.PendingFinalsAfter("")
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
	for nodeID, sessionID := range s.ActiveSessions {
		if strings.TrimSpace(string(nodeID)) != string(nodeID) || nodeID == "" ||
			len(nodeID) > maxNodeID || !utf8.ValidString(string(nodeID)) {
			return errors.New("active-session node identity is invalid")
		}
		if strings.TrimSpace(string(sessionID)) == "" {
			return fmt.Errorf("active session for node %q is invalid", nodeID)
		}
		if _, ok := s.Cards[sessionID]; !ok {
			return fmt.Errorf("active session %q for node %q has no card", sessionID, nodeID)
		}
	}
	for nodeID, sessions := range s.RecentSessions {
		if strings.TrimSpace(string(nodeID)) != string(nodeID) || nodeID == "" || len(nodeID) > maxNodeID || len(sessions) > 512 {
			return errors.New("recent-session node identity or history is invalid")
		}
		seen := make(map[domain.SessionID]struct{}, len(sessions))
		for _, sessionID := range sessions {
			if strings.TrimSpace(string(sessionID)) == "" {
				return errors.New("recent session identity is invalid")
			}
			if _, duplicate := seen[sessionID]; duplicate {
				return errors.New("recent session identity is duplicated")
			}
			seen[sessionID] = struct{}{}
			if _, ok := s.Cards[sessionID]; !ok {
				return fmt.Errorf("recent session %q for node %q has no card", sessionID, nodeID)
			}
		}
	}
	if s.SelectedNode != "" {
		if strings.TrimSpace(string(s.SelectedNode)) != string(s.SelectedNode) ||
			len(s.SelectedNode) > maxNodeID || !utf8.ValidString(string(s.SelectedNode)) {
			return errors.New("selected node identity is invalid")
		}
		selectedActive := s.ActiveSessions[s.SelectedNode]
		if selectedActive != s.ActiveSession {
			return errors.New("selected node active session does not match global projection")
		}
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
		if len(card.PendingFinalOperations) > 512 {
			return errors.New("pending final operations exceed capacity")
		}
		seenFinals := make(map[string]struct{}, len(card.PendingFinalOperations))
		for _, operation := range card.PendingFinalOperations {
			if operation == "" || strings.TrimSpace(operation) != operation || len(operation) > maxAnchor+6 || !utf8.ValidString(operation) {
				return errors.New("pending final operation is invalid")
			}
			if _, duplicate := seenFinals[operation]; duplicate {
				return errors.New("pending final operation is duplicated")
			}
			seenFinals[operation] = struct{}{}
		}
		if len(card.HistoryKeys) != 0 && len(card.HistoryKeys) != len(card.History) {
			return fmt.Errorf("card %q history keys are not aligned", id)
		}
		if len(card.HistoryKinds) != 0 && len(card.HistoryKinds) != len(card.History) {
			return fmt.Errorf("card %q history kinds are not aligned", id)
		}
		if len(card.HistoryTurnKeys) != 0 && len(card.HistoryTurnKeys) != len(card.History) {
			return fmt.Errorf("card %q history turn keys are not aligned", id)
		}
		seenKeys := make(map[string]struct{}, len(card.HistoryKeys))
		for _, key := range card.HistoryKeys {
			if key == "" {
				continue
			}
			if strings.TrimSpace(key) != key || len(key) > maxAnchor || !utf8.ValidString(key) {
				return fmt.Errorf("card %q history key is invalid", id)
			}
			if _, exists := seenKeys[key]; exists {
				return fmt.Errorf("card %q history key is duplicated", id)
			}
			seenKeys[key] = struct{}{}
		}
		for _, item := range card.History {
			if item == "" || !utf8.ValidString(item) || len(item) > 16384 {
				return fmt.Errorf("card %q history item is invalid", id)
			}
		}
		for _, kind := range card.HistoryKinds {
			if kind != "" && kind != "tool" && kind != "thinking" && kind != "final" && kind != "prompt" && kind != "commentary" && kind != "question" {
				return fmt.Errorf("card %q history kind is invalid", id)
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
func (s State) Card(id domain.SessionID) (Card, bool) {
	card, ok := s.Cards[id]
	card.PendingFinalOperations = card.PendingFinalsAfter("")
	return card, ok
}

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
	card.PendingFinalOperations = card.PendingFinalsAfter("")
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
