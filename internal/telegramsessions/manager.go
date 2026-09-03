// Package telegramsessions contains the product-level session list model used
// by Bria menus. It is deliberately independent of Telegram transport and
// provider process ownership.
package telegramsessions

import (
	"errors"
	"sort"

	"bria/internal/domain"
)

// Kind is the way a session is presented in the session menu.
type Kind string

const (
	Active     Kind = "active"
	Background Kind = "background"
	Archived   Kind = "archived"
)

// Item is a stable menu projection. Session contains the full immutable
// logical session so callers can render provider/workdir/status as needed.
type Item struct {
	Session domain.Session
	Kind    Kind
}

// Model owns only selection and menu classification. Every lifecycle state is
// open until Archived/ResumeFailed; background sessions remain selectable and
// are never implicitly stopped when another one is selected.
type Model struct {
	active domain.SessionID
	items  map[domain.SessionID]domain.Session
}

func New(sessions []domain.Session, active domain.SessionID) (Model, error) {
	m := Model{active: active, items: make(map[domain.SessionID]domain.Session, len(sessions))}
	for _, session := range sessions {
		if session.ID() == "" {
			return Model{}, errors.New("session id is required")
		}
		m.items[session.ID()] = session
	}
	hasOpen := false
	for _, session := range m.items {
		if isOpen(session.Status()) {
			hasOpen = true
			break
		}
	}
	if hasOpen && active == "" {
		return Model{}, errors.New("one active session is required while open sessions exist")
	}
	if active != "" {
		session, ok := m.items[active]
		if !ok {
			return Model{}, errors.New("active session is not present")
		}
		if !isOpen(session.Status()) {
			return Model{}, errors.New("active session is archived")
		}
	}
	return m, nil
}

func (m Model) Active() domain.SessionID { return m.active }

// Select changes only the active session. It does not mutate or stop any
// other session, which is the important Bria multi-session invariant.
func (m *Model) Select(id domain.SessionID) error {
	if m == nil {
		return errors.New("nil session model")
	}
	session, ok := m.items[id]
	if !ok {
		return errors.New("session is not present")
	}
	if !isOpen(session.Status()) {
		return errors.New("session is not selectable")
	}
	m.active = id
	return nil
}

// Items returns deterministic menu order: active first, then usable
// backgrounds, then archived sessions, each sorted by logical ID.
func (m Model) Items() []Item {
	ids := make([]domain.SessionID, 0, len(m.items))
	for id := range m.items {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]Item, 0, len(ids))
	for _, id := range ids {
		session := m.items[id]
		kind := Archived
		if isOpen(session.Status()) {
			kind = Background
			if id == m.active {
				kind = Active
			}
		}
		result = append(result, Item{Session: session, Kind: kind})
	}
	sort.SliceStable(result, func(i, j int) bool {
		rank := func(k Kind) int {
			if k == Active {
				return 0
			}
			if k == Background {
				return 1
			}
			return 2
		}
		return rank(result[i].Kind) < rank(result[j].Kind)
	})
	return result
}

func isOpen(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionStarting,
		domain.SessionResuming,
		domain.SessionReady,
		domain.SessionRunning,
		domain.SessionStopping,
		domain.SessionClosingAfterWork,
		domain.SessionAwaitingRecovery,
		domain.SessionClosing:
		return true
	case domain.SessionArchived, domain.SessionResumeFailed:
		return false
	default:
		return false
	}
}

func (m Model) Session(id domain.SessionID) (domain.Session, bool) {
	s, ok := m.items[id]
	return s, ok
}
