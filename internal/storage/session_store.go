// Package storage provides durable local persistence for Bria application state.
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"bria/internal/archiveimport"
	"bria/internal/cardactivity"
	"bria/internal/cardeventhistory"
	"bria/internal/domain"
	"bria/internal/sessionlabel"
	"bria/internal/statejson"
	"bria/internal/telegramhistory"
	"bria/internal/telegramstate"
)

const (
	sessionStoreFormatVersion          = 1
	coordinatorCheckpointFormatVersion = 1
)

var sessionStoreLocks sync.Map

var (
	ErrCompareAndSwapConflict = errors.New("session compare-and-swap conflict")
	ErrInvariantConflict      = errors.New("session invariant conflict")
	ErrSessionNotFound        = errors.New("session not found")
)

// SessionStore persists the latest state of each logical session in one JSON
// file. Operations on one store value are serialized, and a successful write
// has been flushed and atomically renamed before it returns.
type SessionStore struct {
	mu           *sync.Mutex
	path         string
	byIntent     map[domain.IntentID]domain.Session
	byID         map[domain.SessionID]domain.IntentID
	checkpoint   *coordinatorRecord
	telegramUI   *telegramstate.State
	deletedEmpty map[domain.SessionID]domain.Session
	// fullReloads is a deterministic work counter used by package tests. It
	// counts physical state-document decodes, not public Load calls.
	fullReloads uint64
	generation  storeFileGeneration
	writeFile   sessionFileWriter
}

type pathGeneration struct {
	exists bool
	info   os.FileInfo
}

type storeFileGeneration struct {
	state    pathGeneration
	activity pathGeneration
}

type sessionFileWriter func(
	string,
	map[domain.IntentID]domain.Session,
	*coordinatorRecord,
	*telegramstate.State,
) (storeFileGeneration, error)

// OpenSessionStore opens and validates path. A missing file represents an empty
// store and is created by the first successful write.
func OpenSessionStore(path string) (*SessionStore, error) {
	if path == "" {
		return nil, errors.New("session store path is required")
	}
	canonicalPath, err := canonicalStorePath(path)
	if err != nil {
		return nil, err
	}
	mu := mutexForPath(canonicalPath)
	mu.Lock()
	defer mu.Unlock()
	byIntent, byID, checkpoint, ui, generation, err := readVerifiedSessionFile(canonicalPath, true)
	if err != nil {
		return nil, err
	}
	return &SessionStore{
		mu:         mu,
		path:       canonicalPath,
		byIntent:   byIntent,
		byID:       byID,
		checkpoint: checkpoint,
		telegramUI: ui,
		generation: generation,
		writeFile:  writeSessionFile,
	}, nil
}

func canonicalStorePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve session store path: %w", err)
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return "", fmt.Errorf("resolve session store parent: %w", err)
	}
	return filepath.Join(canonicalParent, filepath.Base(absPath)), nil
}

// PutStartingIfAbsent durably inserts session unless its IntentID already has
// a session. A replay returns the existing session without modifying the file.
func (store *SessionStore) PutStartingIfAbsent(
	ctx context.Context,
	session domain.Session,
) (domain.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, false, err
	}
	if err := validateSessionValue(session); err != nil {
		return domain.Session{}, false, fmt.Errorf("validate starting session: %w", err)
	}
	if session.Status() != domain.SessionStarting {
		return domain.Session{}, false, fmt.Errorf(
			"%w: insert status %q, want %q",
			ErrInvariantConflict,
			session.Status(),
			domain.SessionStarting,
		)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.Session{}, false, err
	}
	if err := store.reload(); err != nil {
		return domain.Session{}, false, err
	}
	if existing, ok := store.byIntent[session.IntentID()]; ok {
		return existing, false, nil
	}
	if existingIntent, ok := store.byID[session.ID()]; ok {
		return domain.Session{}, false, fmt.Errorf(
			"%w: session id %q already belongs to intent %q",
			ErrInvariantConflict,
			session.ID(),
			existingIntent,
		)
	}
	if session.Name() != "" {
		name := sessionlabel.Unique(store.byIntent, "", session.Name())
		snapshot := session.Snapshot()
		snapshot.Name = name
		var nameErr error
		session, nameErr = domain.RestoreSession(snapshot)
		if nameErr != nil {
			return domain.Session{}, false, fmt.Errorf("assign unique session name: %w", nameErr)
		}
	}

	next := cloneSessions(store.byIntent)
	next[session.IntentID()] = session
	ui := telegramstate.New()
	if store.telegramUI != nil {
		ui = store.telegramUI.Clone()
	}
	// Only insertion of a new local session establishes known-empty evidence.
	ui.Cards[session.ID()] = telegramstate.Card{SessionID: session.ID(), EmptyCloseEligible: true, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
	if err := store.persist(next, store.checkpoint, &ui); err != nil {
		if reloadErr := store.reload(); reloadErr != nil {
			return domain.Session{}, false, errors.Join(
				fmt.Errorf("persist starting session: %w", err),
				reloadErr,
			)
		}
		return domain.Session{}, false, fmt.Errorf("persist starting session: %w", err)
	}
	store.byIntent = next
	store.telegramUI = &ui
	store.byID[session.ID()] = session.IntentID()
	return session, true, nil
}

// RenameSession atomically assigns the best available short name while
// preserving every lifecycle field of the logical session.
func (store *SessionStore) RenameSession(ctx context.Context, id domain.SessionID, requested string, source domain.SessionNameSource) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return domain.Session{}, err
	}
	intent, ok := store.byID[id]
	if !ok {
		return domain.Session{}, ErrSessionNotFound
	}
	current := store.byIntent[intent]
	name := sessionlabel.Unique(store.byIntent, id, requested)
	next, err := current.Rename(name, source)
	if err != nil {
		return domain.Session{}, fmt.Errorf("validate session name: %w", err)
	}
	if next.Equal(current) {
		return current, nil
	}
	sessions := cloneSessions(store.byIntent)
	sessions[intent] = next
	if err := store.persist(sessions, store.checkpoint, store.telegramUI); err != nil {
		_ = store.reload()
		return domain.Session{}, fmt.Errorf("persist session name: %w", err)
	}
	store.byIntent = sessions
	return next, nil
}

// CompareAndSwap durably replaces expected with next. Repeating an already
// committed transition with the same next state succeeds idempotently.
func (store *SessionStore) CompareAndSwap(
	ctx context.Context,
	expected domain.Session,
	next domain.Session,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionValue(expected); err != nil {
		return fmt.Errorf("validate expected session: %w", err)
	}
	if err := validateSessionValue(next); err != nil {
		return fmt.Errorf("validate next session: %w", err)
	}
	if !sameIdentity(expected, next) {
		return fmt.Errorf("%w: compare-and-swap changes immutable session identity", ErrInvariantConflict)
	}
	if expected.Status() != domain.SessionStarting ||
		(next.Status() != domain.SessionReady && next.Status() != domain.SessionAwaitingRecovery) {
		return fmt.Errorf(
			"%w: unsupported transition %q -> %q",
			ErrInvariantConflict,
			expected.Status(),
			next.Status(),
		)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.reload(); err != nil {
		return err
	}
	current, ok := store.byIntent[expected.IntentID()]
	if !ok {
		return ErrCompareAndSwapConflict
	}
	if current.Equal(next) {
		return nil
	}
	if !current.Equal(expected) {
		return ErrCompareAndSwapConflict
	}

	sessions := cloneSessions(store.byIntent)
	sessions[next.IntentID()] = next
	if err := store.persist(sessions, store.checkpoint, store.telegramUI); err != nil {
		if reloadErr := store.reload(); reloadErr != nil {
			return errors.Join(
				fmt.Errorf("persist session transition: %w", err),
				reloadErr,
			)
		}
		return fmt.Errorf("persist session transition: %w", err)
	}
	store.byIntent = sessions
	return nil
}

// Replace durably replaces any lifecycle state of the same logical session.
// It is used during process recovery, where a persisted provider binding is
// intentionally replaced by the binding of the newly started adapter.
func (store *SessionStore) Replace(ctx context.Context, expected domain.Session, next domain.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSessionValue(expected); err != nil {
		return fmt.Errorf("validate expected session: %w", err)
	}
	if err := validateSessionValue(next); err != nil {
		return fmt.Errorf("validate next session: %w", err)
	}
	if !sameIdentity(expected, next) {
		return fmt.Errorf("%w: replace changes immutable session identity", ErrInvariantConflict)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return err
	}
	current, ok := store.byIntent[expected.IntentID()]
	if !ok || !current.Equal(expected) {
		return ErrCompareAndSwapConflict
	}
	if current.Equal(next) {
		return nil
	}
	sessions := cloneSessions(store.byIntent)
	sessions[next.IntentID()] = next
	ui := store.telegramUI
	// Work submitted outside Telegram is also positive content evidence.
	// Never rely solely on a bounded presentation transcript for emptiness.
	if ui != nil && (hasWorkStatus(current.Status()) || hasWorkStatus(next.Status())) {
		copy := ui.Clone()
		card := copy.Cards[next.ID()]
		card.EmptyCloseEligible = false
		if card.SessionID != "" {
			copy.Cards[next.ID()] = card
		}
		ui = &copy
	}
	if err := store.persist(sessions, store.checkpoint, ui); err != nil {
		return fmt.Errorf("persist session replacement: %w", err)
	}
	store.byIntent = sessions
	store.telegramUI = ui
	return nil
}

func hasWorkStatus(status domain.SessionStatus) bool {
	return status == domain.SessionRunning || status == domain.SessionStopping || status == domain.SessionClosingAfterWork
}

// Load returns the session with id from the latest verified file generation.
func (store *SessionStore) Load(
	ctx context.Context,
	id domain.SessionID,
) (domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return domain.Session{}, err
	}
	intentID, ok := store.byID[id]
	if !ok {
		return domain.Session{}, fmt.Errorf("%w: %q", ErrSessionNotFound, id)
	}
	return store.byIntent[intentID], nil
}

// GetByIntent returns the session stored for intentID from the latest verified
// file generation. It is the exported physical acceptance seam for intent replay.
func (store *SessionStore) GetByIntent(
	ctx context.Context,
	intentID domain.IntentID,
) (domain.Session, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Session{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return domain.Session{}, false, err
	}
	session, ok := store.byIntent[intentID]
	return session, ok, nil
}

// List returns the latest verified generation in ascending IntentID order.
// IntentID order is the stable persisted order used by the state document;
// insertion or map iteration order is never observable.
func (store *SessionStore) List(ctx context.Context) ([]domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return nil, err
	}
	return store.listLoadedSessions(), nil
}

func (store *SessionStore) listLoadedSessions() []domain.Session {
	intentIDs := make([]domain.IntentID, 0, len(store.byIntent))
	for intentID := range store.byIntent {
		intentIDs = append(intentIDs, intentID)
	}
	sort.Slice(intentIDs, func(left, right int) bool {
		return intentIDs[left] < intentIDs[right]
	})
	sessions := make([]domain.Session, 0, len(intentIDs))
	for _, intentID := range intentIDs {
		sessions = append(sessions, store.byIntent[intentID])
	}
	return sessions
}

// ImportArchived atomically adds a complete batch of externally discovered
// provider sessions. It never overwrites an existing logical or provider
// identity. Replaying an identical batch is a no-op and does not rewrite the
// durable file.
func (store *SessionStore) ImportArchived(ctx context.Context, candidates []domain.Session) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.reload(); err != nil {
		return err
	}
	next, changed, err := archiveimport.Merge(store.byIntent, candidates)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvariantConflict, err)
	}
	if !changed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.persist(next, store.checkpoint, store.telegramUI); err != nil {
		return fmt.Errorf("persist archived import: %w", err)
	}
	verifiedByIntent, verifiedByID, checkpoint, ui, generation, err := readVerifiedSessionFile(store.path, true)
	if err != nil {
		return fmt.Errorf("verify archived import: %w", err)
	}
	if !archiveimport.Equal(verifiedByIntent, next) {
		return errors.New("verify archived import: durable state differs")
	}
	store.byIntent = verifiedByIntent
	store.byID = verifiedByID
	store.checkpoint = checkpoint
	store.telegramUI = ui
	store.generation = generation
	return nil
}

func (store *SessionStore) reload() error {
	current, err := inspectStoreGeneration(store.path)
	if err != nil {
		return fmt.Errorf("inspect session store generation: %w", err)
	}
	if sameStoreGeneration(store.generation, current) {
		return nil
	}
	store.fullReloads++
	byIntent, byID, checkpoint, ui, generation, err := readVerifiedSessionFile(store.path, true)
	if err != nil {
		return fmt.Errorf("reload session store: %w", err)
	}
	store.byIntent = byIntent
	store.byID = byID
	store.checkpoint = checkpoint
	store.telegramUI = ui
	store.generation = generation
	return nil
}

func (store *SessionStore) persist(
	sessions map[domain.IntentID]domain.Session,
	checkpoint *coordinatorRecord,
	ui *telegramstate.State,
) error {
	generation, err := store.writeFile(store.path, sessions, checkpoint, ui)
	if err != nil {
		return err
	}
	store.generation = generation
	return nil
}

// LoadTelegramUI returns the durable Telegram state from the latest verified
// file generation. A missing field in an older document migrates to empty.
func (store *SessionStore) LoadTelegramUI(ctx context.Context) (telegramstate.State, error) {
	if err := ctx.Err(); err != nil {
		return telegramstate.State{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return telegramstate.State{}, err
	}
	if store.telegramUI == nil {
		return telegramstate.New(), nil
	}
	return store.telegramUI.Clone(), nil
}

// LoadActiveSession returns the durable selected session, or an empty ID.
func (store *SessionStore) LoadActiveSession(ctx context.Context) (domain.SessionID, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return "", err
	}
	return state.ActiveSession, nil
}

// LoadNodeSelection returns the durable selected node and a copy of the last
// active session recorded for every node.
func (store *SessionStore) LoadNodeSelection(ctx context.Context) (domain.ComputerID, map[domain.ComputerID]domain.SessionID, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return "", nil, err
	}
	active := make(map[domain.ComputerID]domain.SessionID, len(state.ActiveSessions))
	for nodeID, sessionID := range state.ActiveSessions {
		active[nodeID] = sessionID
	}
	return state.SelectedNode, active, nil
}

// LoadNodeSessionHistory returns each node's most-recently-active sessions,
// newest first. Older state files legitimately return an empty history.
func (store *SessionStore) LoadNodeSessionHistory(ctx context.Context) (map[domain.ComputerID][]domain.SessionID, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return nil, err
	}
	history := make(map[domain.ComputerID][]domain.SessionID, len(state.RecentSessions))
	for nodeID, sessions := range state.RecentSessions {
		history[nodeID] = append([]domain.SessionID(nil), sessions...)
	}
	return history, nil
}

// UpdateTelegramUI atomically updates Telegram presentation state in the same
// document as sessions and the coordinator checkpoint.
func (store *SessionStore) UpdateTelegramUI(ctx context.Context, fn func(*telegramstate.State) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("telegram UI update function is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.reload(); err != nil {
		return err
	}
	next := telegramstate.New()
	if store.telegramUI != nil {
		next = store.telegramUI.Clone()
	}
	if err := fn(&next); err != nil {
		return err
	}
	if err := next.Validate(); err != nil {
		return fmt.Errorf("validate Telegram UI state: %w", err)
	}
	if err := store.persist(store.byIntent, store.checkpoint, &next); err != nil {
		return fmt.Errorf("persist Telegram UI state: %w", err)
	}
	store.telegramUI = &next
	return nil
}

// SetActiveSession persists the selected session and initializes its card
// position without touching the coordinator checkpoint.
func (store *SessionStore) SetActiveSession(ctx context.Context, sessionID domain.SessionID) error {
	if strings.TrimSpace(string(sessionID)) == "" {
		return errors.New("active session id is required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		state.ActiveSession = sessionID
		if state.SelectedNode != "" {
			if state.ActiveSessions == nil {
				state.ActiveSessions = make(map[domain.ComputerID]domain.SessionID)
			}
			state.ActiveSessions[state.SelectedNode] = sessionID
			if state.RecentSessions == nil {
				state.RecentSessions = make(map[domain.ComputerID][]domain.SessionID)
			}
			state.RecentSessions[state.SelectedNode] = promoteRecentSession(state.RecentSessions[state.SelectedNode], sessionID)
		}
		if state.Cards == nil {
			state.Cards = make(map[domain.SessionID]telegramstate.Card)
		}
		if _, ok := state.Cards[sessionID]; !ok {
			state.Cards[sessionID] = telegramstate.Card{
				SessionID: sessionID,
				Page:      telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
			}
		}
		return nil
	})
}

// SetSelectedNode persists the current node and projects that node's last
// active session into the legacy ActiveSession field.
func (store *SessionStore) SetSelectedNode(ctx context.Context, nodeID domain.ComputerID) error {
	if strings.TrimSpace(string(nodeID)) == "" {
		return errors.New("selected node id is required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		state.SelectedNode = nodeID
		state.ActiveSession = state.ActiveSessions[nodeID]
		return nil
	})
}

// SetNodeActiveSession selects nodeID and persists its last active session.
func (store *SessionStore) SetNodeActiveSession(ctx context.Context, nodeID domain.ComputerID, sessionID domain.SessionID) error {
	return store.setNodeActiveSession(ctx, nodeID, sessionID, true)
}

// SetNodeLastActiveSession updates one node without navigating the foreground.
func (store *SessionStore) SetNodeLastActiveSession(ctx context.Context, nodeID domain.ComputerID, sessionID domain.SessionID) error {
	return store.setNodeActiveSession(ctx, nodeID, sessionID, false)
}

func (store *SessionStore) setNodeActiveSession(ctx context.Context, nodeID domain.ComputerID, sessionID domain.SessionID, navigate bool) error {
	if strings.TrimSpace(string(nodeID)) == "" || strings.TrimSpace(string(sessionID)) == "" {
		return errors.New("node and active session ids are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		if navigate {
			state.SelectedNode = nodeID
		}
		if state.SelectedNode == nodeID {
			state.ActiveSession = sessionID
		}
		if state.ActiveSessions == nil {
			state.ActiveSessions = make(map[domain.ComputerID]domain.SessionID)
		}
		state.ActiveSessions[nodeID] = sessionID
		if state.RecentSessions == nil {
			state.RecentSessions = make(map[domain.ComputerID][]domain.SessionID)
		}
		state.RecentSessions[nodeID] = promoteRecentSession(state.RecentSessions[nodeID], sessionID)
		if state.Cards == nil {
			state.Cards = make(map[domain.SessionID]telegramstate.Card)
		}
		if _, ok := state.Cards[sessionID]; !ok {
			state.Cards[sessionID] = telegramstate.Card{
				SessionID: sessionID,
				Page:      telegramstate.Page{Current: 1, Total: 1, FollowLatest: true},
			}
		}
		return nil
	})
}

func promoteRecentSession(history []domain.SessionID, sessionID domain.SessionID) []domain.SessionID {
	result := make([]domain.SessionID, 1, len(history)+1)
	result[0] = sessionID
	for _, candidate := range history {
		if candidate != sessionID {
			result = append(result, candidate)
		}
	}
	if len(result) > 512 {
		result = result[:512]
	}
	return result
}

// ClearNodeActiveSession removes one node's active-session projection while
// retaining the selected node itself.
func (store *SessionStore) ClearNodeActiveSession(ctx context.Context, nodeID domain.ComputerID) error {
	if strings.TrimSpace(string(nodeID)) == "" {
		return errors.New("node id is required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		delete(state.ActiveSessions, nodeID)
		if state.SelectedNode == nodeID {
			state.ActiveSession = ""
		}
		return nil
	})
}

func (store *SessionStore) SetCardCarrier(ctx context.Context, sessionID domain.SessionID, chatID, messageID int64) error {
	if sessionID == "" || chatID <= 0 || messageID <= 0 {
		return errors.New("session and positive card carrier identifiers are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[sessionID]
		if !ok {
			card = telegramstate.Card{SessionID: sessionID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
		}
		next := telegramstate.Carrier{ChatID: chatID, MessageID: messageID}
		if card.Carrier != next {
			card.CarrierOperation = ""
		}
		card.Carrier = next
		return state.SetCard(card)
	})
}

func (store *SessionStore) SetCardPage(ctx context.Context, sessionID domain.SessionID, current, total int, anchor string, followLatest bool) error {
	if sessionID == "" || current < 1 || total < current {
		return errors.New("valid card page is required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[sessionID]
		if !ok {
			card = telegramstate.Card{SessionID: sessionID}
		}
		card.Page = telegramstate.Page{Current: current, Total: total, Anchor: anchor, FollowLatest: followLatest}
		return state.SetCard(card)
	})
}

func (store *SessionStore) AppendCardHistory(ctx context.Context, sessionID domain.SessionID, item string) error {
	return cardeventhistory.Append(ctx, store, sessionID, item, "")
}

// AppendCardTechnicalHistory retains one exact provider tool event. Technical
// identity is explicit metadata and is never inferred from visible text.
func (store *SessionStore) AppendCardTechnicalHistory(ctx context.Context, sessionID domain.SessionID, item string) error {
	return cardeventhistory.Append(ctx, store, sessionID, item, "tool")
}

// SetCardPrompt inserts or replaces one user prompt at its stable message ID.
// The visible history remains plain text; the parallel key is presentation
// metadata used only to update the three-state prompt marker.
func (store *SessionStore) SetCardPrompt(ctx context.Context, sessionID domain.SessionID, messageID, item string) error {
	if sessionID == "" || strings.TrimSpace(messageID) == "" || messageID != strings.TrimSpace(messageID) || item == "" {
		return errors.New("session, prompt message, and history item are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		intent, exists := store.byID[sessionID]
		if !exists {
			return ErrSessionNotFound
		}
		session := store.byIntent[intent]
		if session.Status() == domain.SessionClosing || session.Status() == domain.SessionArchived || session.Status() == domain.SessionAwaitingRecovery && !slices.Contains(state.Cards[sessionID].HistoryKeys, messageID) {
			return fmt.Errorf("cannot accept request into %s session", session.Status())
		}
		card, ok := state.Cards[sessionID]
		if !ok {
			card = telegramstate.Card{SessionID: sessionID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
		}
		card.EmptyCloseEligible = false
		for index, key := range card.HistoryKeys {
			if key == messageID {
				if card.History[index] == item {
					return nil
				}
				card.History[index] = item
				card.TouchEvent(time.Now())
				return state.SetCard(card)
			}
		}
		telegramhistory.Append(&card, item, "")
		if len(card.HistoryKeys) == 0 {
			card.HistoryKeys = make([]string, len(card.History))
		}
		card.HistoryKeys[len(card.HistoryKeys)-1] = messageID
		card.TouchEvent(time.Now())
		return state.SetCard(card)
	})
}

func (store *SessionStore) LoadCardHistory(ctx context.Context, sessionID domain.SessionID) ([]string, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return nil, err
	}
	card, ok := state.Card(sessionID)
	if !ok {
		return nil, nil
	}
	return append([]string(nil), card.History...), nil
}

// LoadCardDisplayHistory returns an isolated presentation copy. Legacy cards
// without kind metadata contain only ordinary entries. Hiding technical
// actions removes only entries explicitly marked as tool events.
func (store *SessionStore) LoadCardDisplayHistory(ctx context.Context, sessionID domain.SessionID, showTechnical bool) ([]string, error) {
	state, err := store.LoadTelegramUI(ctx)
	if err != nil {
		return nil, err
	}
	card, ok := state.Card(sessionID)
	if !ok {
		return nil, nil
	}
	if showTechnical || len(card.HistoryKinds) == 0 {
		return append([]string(nil), card.History...), nil
	}
	history := make([]string, 0, len(card.History))
	for index, item := range card.History {
		if card.HistoryKinds[index] != "tool" {
			history = append(history, item)
		}
	}
	return history, nil
}

type sessionFile struct {
	Version     int                  `json:"version"`
	Sessions    []sessionRecord      `json:"sessions"`
	Coordinator *coordinatorRecord   `json:"coordinator,omitempty"`
	TelegramUI  *telegramstate.State `json:"telegram_ui,omitempty"`
}

type coordinatorRecord struct {
	Version      int                      `json:"version"`
	Revision     uint64                   `json:"revision"`
	Initialized  bool                     `json:"initialized"`
	NextUpdateID int64                    `json:"next_update_id"`
	Blocked      *blockedUpdateRecord     `json:"blocked,omitempty"`
	Outbound     *outboundOperationRecord `json:"outbound,omitempty"`
	Recovery     *recoveryControlRecord   `json:"recovery,omitempty"`
}

type recoveryControlRecord struct {
	OriginalOperationID string `json:"original_operation_id"`
	PromptOperationID   string `json:"prompt_operation_id"`
	UpdateID            int64  `json:"update_id"`
}

type blockedUpdateRecord struct {
	UpdateID int64  `json:"update_id"`
	Reason   string `json:"reason"`
}

type outboundOperationRecord struct {
	OperationID string                        `json:"operation_id"`
	UpdateID    int64                         `json:"update_id"`
	Status      statusRecord                  `json:"status"`
	Keyboard    [][]keyboardButtonRecord      `json:"keyboard,omitempty"`
	Phase       string                        `json:"phase"`
	Receipt     *receiptRecord                `json:"receipt,omitempty"`
	Durable     *durableOutboundReceiptRecord `json:"durable,omitempty"`
}

type keyboardButtonRecord struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type durableOutboundReceiptRecord struct {
	OperationID string `json:"operation_id"`
	Sequence    uint64 `json:"sequence"`
}

type statusRecord struct {
	ScreenSessionID string `json:"screen_session_id,omitempty"`
	ConversationID  int64  `json:"conversation_id"`
	Text            string `json:"text"`
	RichMarkdown    bool   `json:"rich_markdown,omitempty"`
	CallbackQueryID string `json:"callback_query_id,omitempty"`
	SourceMessageID int64  `json:"source_message_id,omitempty"`
}

type receiptRecord struct {
	MessageID int64 `json:"message_id"`
}

type sessionRecord struct {
	ID             domain.SessionID         `json:"id"`
	IntentID       domain.IntentID          `json:"intent_id"`
	ComputerID     domain.ComputerID        `json:"computer_id"`
	Provider       domain.Provider          `json:"provider"`
	Workdir        string                   `json:"workdir"`
	Name           string                   `json:"name,omitempty"`
	NameSource     domain.SessionNameSource `json:"name_source,omitempty"`
	Model          string                   `json:"model,omitempty"`
	Effort         string                   `json:"effort,omitempty"`
	Status         domain.SessionStatus     `json:"status"`
	Binding        *bindingRecord           `json:"binding,omitempty"`
	CreatedAt      time.Time                `json:"created_at,omitempty"`
	LastResumedAt  *time.Time               `json:"last_resumed_at,omitempty"`
	StateChangedAt time.Time                `json:"state_changed_at,omitempty"`
	Lifetime       domain.SessionLifetime   `json:"lifetime,omitempty"`
	DeadlineAt     *time.Time               `json:"deadline_at,omitempty"`
	RecoveryTarget *domain.SessionStatus    `json:"recovery_target,omitempty"`
}

type bindingRecord struct {
	Provider   domain.Provider `json:"provider"`
	SessionID  string          `json:"session_id"`
	Generation uint64          `json:"generation"`
}

func recordFromSession(session domain.Session) sessionRecord {
	snapshot := session.Snapshot()
	record := sessionRecord{
		ID:             snapshot.ID,
		IntentID:       snapshot.IntentID,
		ComputerID:     snapshot.ComputerID,
		Provider:       snapshot.Provider,
		Workdir:        snapshot.Workdir,
		Name:           snapshot.Name,
		NameSource:     snapshot.NameSource,
		Model:          snapshot.Model,
		Effort:         snapshot.Effort,
		Status:         snapshot.Status,
		CreatedAt:      snapshot.CreatedAt,
		LastResumedAt:  cloneTime(snapshot.LastResumedAt),
		StateChangedAt: snapshot.StateChangedAt,
		Lifetime:       snapshot.Lifetime,
		DeadlineAt:     cloneTime(snapshot.DeadlineAt),
		RecoveryTarget: cloneSessionStatus(snapshot.RecoveryTarget),
	}
	if snapshot.Binding != nil {
		record.Binding = &bindingRecord{
			Provider:   snapshot.Binding.Provider,
			SessionID:  snapshot.Binding.SessionID,
			Generation: snapshot.Binding.Generation,
		}
	}
	return record
}

func (record sessionRecord) restore() (domain.Session, error) {
	snapshot := domain.SessionSnapshot{
		ID:             record.ID,
		IntentID:       record.IntentID,
		ComputerID:     record.ComputerID,
		Provider:       record.Provider,
		Workdir:        record.Workdir,
		Name:           record.Name,
		NameSource:     record.NameSource,
		Model:          record.Model,
		Effort:         record.Effort,
		Status:         record.Status,
		CreatedAt:      record.CreatedAt,
		LastResumedAt:  cloneTime(record.LastResumedAt),
		StateChangedAt: record.StateChangedAt,
		Lifetime:       record.Lifetime,
		DeadlineAt:     cloneTime(record.DeadlineAt),
		RecoveryTarget: cloneSessionStatus(record.RecoveryTarget),
	}
	if record.Binding != nil {
		snapshot.Binding = &domain.ProviderBinding{
			Provider:   record.Binding.Provider,
			SessionID:  record.Binding.SessionID,
			Generation: record.Binding.Generation,
		}
	}
	return domain.RestoreSession(snapshot)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneSessionStatus(value *domain.SessionStatus) *domain.SessionStatus {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// ValidateSessionFile uses the exact decoder without repairing permissions or
// writing state. A rollback probe must be safe against a running store.
func ValidateSessionFile(path string) error {
	canonical, err := canonicalStorePath(path)
	if err != nil {
		return err
	}
	_, _, _, _, err = readSessionFile(canonical, false)
	return err
}

func readVerifiedSessionFile(
	path string, repairPermissions bool,
) (map[domain.IntentID]domain.Session, map[domain.SessionID]domain.IntentID, *coordinatorRecord, *telegramstate.State, storeFileGeneration, error) {
	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		before, err := inspectStoreGeneration(path)
		if err != nil {
			return nil, nil, nil, nil, storeFileGeneration{}, err
		}
		byIntent, byID, checkpoint, ui, err := readSessionFile(path, repairPermissions)
		if err != nil {
			return nil, nil, nil, nil, storeFileGeneration{}, err
		}
		after, err := inspectStoreGeneration(path)
		if err != nil {
			return nil, nil, nil, nil, storeFileGeneration{}, err
		}
		if sameStoreGeneration(before, after) {
			return byIntent, byID, checkpoint, ui, after, nil
		}
	}
	return nil, nil, nil, nil, storeFileGeneration{}, errors.New("session store changed while it was being read")
}

func inspectStoreGeneration(path string) (storeFileGeneration, error) {
	state, err := inspectPathGeneration(path)
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("inspect state document: %w", err)
	}
	activity, err := inspectPathGeneration(path + ".card-activity.json")
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("inspect card activity: %w", err)
	}
	return storeFileGeneration{state: state, activity: activity}, nil
}

func inspectPathGeneration(path string) (pathGeneration, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return pathGeneration{}, nil
	}
	if err != nil {
		return pathGeneration{}, err
	}
	return pathGeneration{exists: true, info: info}, nil
}

func sameStoreGeneration(left, right storeFileGeneration) bool {
	return samePathGeneration(left.state, right.state) &&
		samePathGeneration(left.activity, right.activity)
}

func samePathGeneration(left, right pathGeneration) bool {
	if left.exists != right.exists {
		return false
	}
	if !left.exists {
		return true
	}
	return os.SameFile(left.info, right.info) &&
		left.info.Size() == right.info.Size() &&
		left.info.ModTime().Equal(right.info.ModTime()) &&
		left.info.Mode() == right.info.Mode()
}

func readSessionFile(
	path string, repairPermissions bool,
) (map[domain.IntentID]domain.Session, map[domain.SessionID]domain.IntentID, *coordinatorRecord, *telegramstate.State, error) {
	byIntent := make(map[domain.IntentID]domain.Session)
	byID := make(map[domain.SessionID]domain.IntentID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return byIntent, byID, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("inspect session store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, nil, nil, fmt.Errorf("session store %q is not a regular file", path)
	}
	if repairPermissions {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("secure session store permissions: %w", err)
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open session store: %w", err)
	}
	defer file.Close()
	if err := rejectDuplicateJSONKeys(file); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("decode session store: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("rewind session store: %w", err)
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var persisted sessionFile
	if err := decoder.Decode(&persisted); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("decode session store: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, nil, nil, errors.New("decode session store: trailing JSON value")
		}
		return nil, nil, nil, nil, fmt.Errorf("decode session store trailing data: %w", err)
	}
	if persisted.Version != sessionStoreFormatVersion {
		return nil, nil, nil, nil, fmt.Errorf(
			"unsupported session store version %d, want %d",
			persisted.Version,
			sessionStoreFormatVersion,
		)
	}
	if persisted.Coordinator != nil && persisted.Coordinator.Version != coordinatorCheckpointFormatVersion {
		return nil, nil, nil, nil, fmt.Errorf(
			"unsupported coordinator checkpoint version %d, want %d",
			persisted.Coordinator.Version,
			coordinatorCheckpointFormatVersion,
		)
	}
	if persisted.Coordinator != nil {
		if _, err := storedCheckpointFromRecord(persisted.Coordinator); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("validate coordinator checkpoint: %w", err)
		}
	}
	for index, record := range persisted.Sessions {
		session, err := record.restore()
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("restore session record %d: %w", index, err)
		}
		if _, exists := byIntent[session.IntentID()]; exists {
			return nil, nil, nil, nil, fmt.Errorf("duplicate intent id %q", session.IntentID())
		}
		if existingIntent, exists := byID[session.ID()]; exists {
			return nil, nil, nil, nil, fmt.Errorf(
				"duplicate session id %q for intents %q and %q",
				session.ID(),
				existingIntent,
				session.IntentID(),
			)
		}
		byIntent[session.IntentID()] = session
		byID[session.ID()] = session.IntentID()
	}
	ui := persisted.TelegramUI
	if ui != nil {
		if err := cardactivity.Apply(path, ui); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("load card activity: %w", err)
		}
		if err := ui.Validate(); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("validate Telegram UI state: %w", err)
		}
		ui = ptrUI(ui.Clone())
	}
	return byIntent, byID, cloneCoordinatorRecord(persisted.Coordinator), ui, nil
}

func ptrUI(s telegramstate.State) *telegramstate.State { return &s }

func rejectDuplicateJSONKeys(reader io.Reader) error { return statejson.ValidateKeys(reader) }

func writeSessionFile(
	path string,
	sessions map[domain.IntentID]domain.Session,
	checkpoint *coordinatorRecord,
	telegramUI *telegramstate.State,
) (generation storeFileGeneration, returnErr error) {
	records := make([]sessionRecord, 0, len(sessions))
	for _, session := range sessions {
		records = append(records, recordFromSession(session))
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].IntentID < records[right].IntentID
	})
	data, err := json.MarshalIndent(sessionFile{
		Version:     sessionStoreFormatVersion,
		Sessions:    records,
		Coordinator: cloneCoordinatorRecord(checkpoint),
		TelegramUI:  cardactivity.MainState(telegramUI),
	}, "", "  ")
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("encode session store: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("create session store candidate: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = fmt.Errorf("close session store candidate: %w", closeErr)
			}
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return storeFileGeneration{}, fmt.Errorf("secure session store candidate: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return storeFileGeneration{}, fmt.Errorf("write session store candidate: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return storeFileGeneration{}, fmt.Errorf("sync session store candidate: %w", err)
	}
	writtenInfo, err := temporary.Stat()
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("inspect session store candidate: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return storeFileGeneration{}, fmt.Errorf("close session store candidate: %w", err)
	}
	temporaryOpen = false
	if err := cardactivity.Save(path, telegramUI); err != nil {
		return storeFileGeneration{}, fmt.Errorf("persist card activity: %w", err)
	}
	writtenActivity, err := inspectPathGeneration(path + ".card-activity.json")
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("inspect persisted card activity: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return storeFileGeneration{}, fmt.Errorf("replace session store: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("open session store directory: %w", err)
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		return storeFileGeneration{}, fmt.Errorf("sync session store directory: %w", err)
	}
	if err := directoryFile.Close(); err != nil {
		return storeFileGeneration{}, fmt.Errorf("close session store directory: %w", err)
	}
	current, err := inspectStoreGeneration(path)
	if err != nil {
		return storeFileGeneration{}, fmt.Errorf("verify persisted session store generation: %w", err)
	}
	writtenState := pathGeneration{exists: true, info: writtenInfo}
	if !samePathGeneration(writtenState, current.state) ||
		!samePathGeneration(writtenActivity, current.activity) {
		return storeFileGeneration{}, errors.New("session store changed before write verification completed")
	}
	return current, nil
}

func cloneCoordinatorRecord(source *coordinatorRecord) *coordinatorRecord {
	if source == nil {
		return nil
	}
	clone := *source
	if source.Blocked != nil {
		blocked := *source.Blocked
		clone.Blocked = &blocked
	}
	if source.Recovery != nil {
		recovery := *source.Recovery
		clone.Recovery = &recovery
	}
	if source.Outbound != nil {
		outbound := *source.Outbound
		clone.Outbound = &outbound
		if source.Outbound.Receipt != nil {
			receipt := *source.Outbound.Receipt
			clone.Outbound.Receipt = &receipt
		}
	}
	return &clone
}

func validateSessionValue(session domain.Session) error {
	restored, err := domain.RestoreSession(session.Snapshot())
	if err != nil {
		return err
	}
	if !restored.Equal(session) {
		return errors.New("session value does not match its snapshot")
	}
	return nil
}

func sameIdentity(left, right domain.Session) bool {
	return left.ID() == right.ID() &&
		left.IntentID() == right.IntentID() &&
		left.ComputerID() == right.ComputerID() &&
		left.Provider() == right.Provider() &&
		left.Workdir() == right.Workdir()
}

func cloneSessions(source map[domain.IntentID]domain.Session) map[domain.IntentID]domain.Session {
	clone := make(map[domain.IntentID]domain.Session, len(source)+1)
	for intentID, session := range source {
		clone[intentID] = session
	}
	return clone
}

func mutexForPath(path string) *sync.Mutex {
	lock, _ := sessionStoreLocks.LoadOrStore(path, &sync.Mutex{})
	return lock.(*sync.Mutex)
}
