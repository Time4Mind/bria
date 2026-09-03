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
	"sort"
	"strings"
	"sync"
	"time"

	"bria/internal/archiveimport"
	"bria/internal/domain"
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
	mu         *sync.Mutex
	path       string
	byIntent   map[domain.IntentID]domain.Session
	byID       map[domain.SessionID]domain.IntentID
	checkpoint *coordinatorRecord
	telegramUI *telegramstate.State
}

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
	byIntent, byID, checkpoint, ui, err := readSessionFile(canonicalPath)
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

	next := cloneSessions(store.byIntent)
	next[session.IntentID()] = session
	if err := writeSessionFile(store.path, next, store.checkpoint, store.telegramUI); err != nil {
		if reloadErr := store.reload(); reloadErr != nil {
			return domain.Session{}, false, errors.Join(
				fmt.Errorf("persist starting session: %w", err),
				reloadErr,
			)
		}
		return domain.Session{}, false, fmt.Errorf("persist starting session: %w", err)
	}
	store.byIntent = next
	store.byID[session.ID()] = session.IntentID()
	return session, true, nil
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
	if err := writeSessionFile(store.path, sessions, store.checkpoint, store.telegramUI); err != nil {
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
	if err := writeSessionFile(store.path, sessions, store.checkpoint, store.telegramUI); err != nil {
		return fmt.Errorf("persist session replacement: %w", err)
	}
	store.byIntent = sessions
	return nil
}

// Load rereads the durable file and returns the session with id.
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

// GetByIntent rereads the durable file and returns the session stored for
// intentID. It is the exported physical acceptance seam for intent replay.
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

// List rereads the durable document and returns all sessions in ascending
// IntentID order. IntentID order is the stable persisted order used by the
// state document; insertion or map iteration order is never observable.
func (store *SessionStore) List(ctx context.Context) ([]domain.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.reload(); err != nil {
		return nil, err
	}
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
	return sessions, nil
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
	if err := writeSessionFile(store.path, next, store.checkpoint, store.telegramUI); err != nil {
		return fmt.Errorf("persist archived import: %w", err)
	}
	verifiedByIntent, verifiedByID, checkpoint, ui, err := readSessionFile(store.path)
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
	return nil
}

func (store *SessionStore) reload() error {
	byIntent, byID, checkpoint, ui, err := readSessionFile(store.path)
	if err != nil {
		return fmt.Errorf("reload session store: %w", err)
	}
	store.byIntent = byIntent
	store.byID = byID
	store.checkpoint = checkpoint
	store.telegramUI = ui
	return nil
}

// LoadTelegramUI rereads and returns the durable Telegram presentation state.
// A missing field in an older session document is migrated to an empty state.
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
	if err := writeSessionFile(store.path, store.byIntent, store.checkpoint, &next); err != nil {
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

// SetCardCarrier durably records the Telegram message carrying a session card.
func (store *SessionStore) SetCardCarrier(ctx context.Context, sessionID domain.SessionID, chatID, messageID int64) error {
	if sessionID == "" || chatID <= 0 || messageID <= 0 {
		return errors.New("session and positive card carrier identifiers are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[sessionID]
		if !ok {
			card = telegramstate.Card{SessionID: sessionID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
		}
		card.Carrier = telegramstate.Carrier{ChatID: chatID, MessageID: messageID}
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
	if sessionID == "" || item == "" {
		return errors.New("session and history item are required")
	}
	return store.UpdateTelegramUI(ctx, func(state *telegramstate.State) error {
		card, ok := state.Cards[sessionID]
		if !ok {
			card = telegramstate.Card{SessionID: sessionID, Page: telegramstate.Page{Current: 1, Total: 1, FollowLatest: true}}
		}
		if len(card.History) >= 512 {
			card.History = append([]string(nil), card.History[len(card.History)-511:]...)
		}
		card.History = append(card.History, item)
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
	ConversationID  int64  `json:"conversation_id"`
	Text            string `json:"text"`
	CallbackQueryID string `json:"callback_query_id,omitempty"`
	SourceMessageID int64  `json:"source_message_id,omitempty"`
}

type receiptRecord struct {
	MessageID int64 `json:"message_id"`
}

type sessionRecord struct {
	ID             domain.SessionID       `json:"id"`
	IntentID       domain.IntentID        `json:"intent_id"`
	ComputerID     domain.ComputerID      `json:"computer_id"`
	Provider       domain.Provider        `json:"provider"`
	Workdir        string                 `json:"workdir"`
	Status         domain.SessionStatus   `json:"status"`
	Binding        *bindingRecord         `json:"binding,omitempty"`
	CreatedAt      time.Time              `json:"created_at,omitempty"`
	LastResumedAt  *time.Time             `json:"last_resumed_at,omitempty"`
	StateChangedAt time.Time              `json:"state_changed_at,omitempty"`
	Lifetime       domain.SessionLifetime `json:"lifetime,omitempty"`
	DeadlineAt     *time.Time             `json:"deadline_at,omitempty"`
	RecoveryTarget *domain.SessionStatus  `json:"recovery_target,omitempty"`
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

func readSessionFile(
	path string,
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
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("secure session store permissions: %w", err)
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
		if err := ui.Validate(); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("validate Telegram UI state: %w", err)
		}
		ui = ptrUI(ui.Clone())
	}
	return byIntent, byID, cloneCoordinatorRecord(persisted.Coordinator), ui, nil
}

func ptrUI(s telegramstate.State) *telegramstate.State { return &s }

func rejectDuplicateJSONKeys(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	var readValue func() error
	readValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if key != strings.ToLower(key) {
					return fmt.Errorf("non-canonical object key %q", key)
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return errors.New("object has invalid closing delimiter")
			}
		case '[':
			for decoder.More() {
				if err := readValue(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return errors.New("array has invalid closing delimiter")
			}
		default:
			return fmt.Errorf("unexpected delimiter %q", delimiter)
		}
		return nil
	}
	return readValue()
}

func writeSessionFile(
	path string,
	sessions map[domain.IntentID]domain.Session,
	checkpoint *coordinatorRecord,
	telegramUI *telegramstate.State,
) (returnErr error) {
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
		TelegramUI:  cloneUI(telegramUI),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session store: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create session store candidate: %w", err)
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
		return fmt.Errorf("secure session store candidate: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write session store candidate: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync session store candidate: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session store candidate: %w", err)
	}
	temporaryOpen = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace session store: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open session store directory: %w", err)
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		return fmt.Errorf("sync session store directory: %w", err)
	}
	if err := directoryFile.Close(); err != nil {
		return fmt.Errorf("close session store directory: %w", err)
	}
	return nil
}

func cloneUI(source *telegramstate.State) *telegramstate.State {
	if source == nil {
		return nil
	}
	clone := source.Clone()
	return &clone
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
