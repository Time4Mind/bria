package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"bria/internal/domain"
)

const telegramReplyRouteFormatVersion = 1

var (
	// ErrReplyRouteConflict means a Telegram message already belongs to a
	// different logical session. Reassigning it would make old replies unsafe.
	ErrReplyRouteConflict = errors.New("Telegram reply route conflict")
	// ErrReplyRouteScope means the persisted file belongs to another owner or
	// private chat and must not be used for reply routing in this process.
	ErrReplyRouteScope = errors.New("Telegram reply route scope mismatch")
)

// TelegramOutboundReceipt is the minimum confirmed Telegram delivery result
// needed to route a future reply back to the logical session that produced it.
type TelegramOutboundReceipt struct {
	MessageID int64
	SessionID domain.SessionID
}

// TelegramReplyRouteStore durably binds confirmed outbound Telegram messages
// to sessions for exactly one configured owner and private chat. It implements
// telegramcontroller.ReplyRouteStore structurally without importing that layer.
type TelegramReplyRouteStore struct {
	mu            *sync.Mutex
	path          string
	ownerUserID   int64
	privateChatID int64
	routes        map[int64]domain.SessionID
}

// OpenTelegramReplyRouteStore opens a route file scoped to one owner and one
// private chat. A missing file is created by the first confirmed receipt.
func OpenTelegramReplyRouteStore(path string, ownerUserID, privateChatID int64) (*TelegramReplyRouteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("Telegram reply route store path is required")
	}
	if ownerUserID <= 0 || privateChatID <= 0 {
		return nil, errors.New("positive Telegram owner and private chat identities are required")
	}
	canonicalPath, err := canonicalStorePath(path)
	if err != nil {
		return nil, fmt.Errorf("resolve Telegram reply route store path: %w", err)
	}
	mu := mutexForPath(canonicalPath)
	mu.Lock()
	defer mu.Unlock()
	routes, err := readTelegramReplyRoutes(canonicalPath, ownerUserID, privateChatID)
	if err != nil {
		return nil, err
	}
	return &TelegramReplyRouteStore{
		mu:            mu,
		path:          canonicalPath,
		ownerUserID:   ownerUserID,
		privateChatID: privateChatID,
		routes:        routes,
	}, nil
}

// RecordOutboundReceipt durably records a confirmed Telegram message. Repeating
// the same receipt is idempotent; assigning that message to another session is
// rejected because the historic reply target must never change.
func (store *TelegramReplyRouteStore) RecordOutboundReceipt(ctx context.Context, receipt TelegramOutboundReceipt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTelegramOutboundReceipt(receipt); err != nil {
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
	if existing, ok := store.routes[receipt.MessageID]; ok {
		if existing == receipt.SessionID {
			return nil
		}
		return fmt.Errorf(
			"%w: message %d belongs to session %q, not %q",
			ErrReplyRouteConflict,
			receipt.MessageID,
			existing,
			receipt.SessionID,
		)
	}

	next := cloneTelegramReplyRoutes(store.routes)
	next[receipt.MessageID] = receipt.SessionID
	if err := writeTelegramReplyRoutes(store.path, store.ownerUserID, store.privateChatID, next); err != nil {
		return fmt.Errorf("persist Telegram reply route: %w", err)
	}
	persisted, err := readTelegramReplyRoutes(store.path, store.ownerUserID, store.privateChatID)
	if err != nil {
		return fmt.Errorf("reread Telegram reply route: %w", err)
	}
	if !reflect.DeepEqual(persisted, next) {
		return errors.New("reread Telegram reply route: persisted value mismatch")
	}
	store.routes = persisted
	return nil
}

// ResolveReply returns the session that produced messageID in this store's
// configured owner/private-chat scope.
func (store *TelegramReplyRouteStore) ResolveReply(ctx context.Context, messageID int64) (domain.SessionID, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if messageID <= 0 {
		return "", false, errors.New("positive Telegram reply message id is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := store.reload(); err != nil {
		return "", false, err
	}
	sessionID, ok := store.routes[messageID]
	return sessionID, ok, nil
}

func (store *TelegramReplyRouteStore) reload() error {
	routes, err := readTelegramReplyRoutes(store.path, store.ownerUserID, store.privateChatID)
	if err != nil {
		return err
	}
	store.routes = routes
	return nil
}

type telegramReplyRouteFile struct {
	Version       int                        `json:"version"`
	OwnerUserID   int64                      `json:"owner_user_id"`
	PrivateChatID int64                      `json:"private_chat_id"`
	Routes        []telegramReplyRouteRecord `json:"routes"`
}

type telegramReplyRouteRecord struct {
	MessageID int64            `json:"message_id"`
	SessionID domain.SessionID `json:"session_id"`
}

func readTelegramReplyRoutes(path string, ownerUserID, privateChatID int64) (map[int64]domain.SessionID, error) {
	routes := make(map[int64]domain.SessionID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return routes, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Telegram reply route store: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Telegram reply route store %q is not a regular file", path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure Telegram reply route store permissions: %w", err)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Telegram reply route store: %w", err)
	}
	defer file.Close()
	if err := rejectDuplicateJSONKeys(file); err != nil {
		return nil, fmt.Errorf("decode Telegram reply route store: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind Telegram reply route store: %w", err)
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var persisted telegramReplyRouteFile
	if err := decoder.Decode(&persisted); err != nil {
		return nil, fmt.Errorf("decode Telegram reply route store: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode Telegram reply route store: trailing JSON value")
		}
		return nil, fmt.Errorf("decode Telegram reply route store trailing data: %w", err)
	}
	if persisted.Version != telegramReplyRouteFormatVersion {
		return nil, fmt.Errorf(
			"unsupported Telegram reply route store version %d, want %d",
			persisted.Version,
			telegramReplyRouteFormatVersion,
		)
	}
	if persisted.OwnerUserID != ownerUserID || persisted.PrivateChatID != privateChatID {
		return nil, fmt.Errorf(
			"%w: persisted owner/chat %d/%d, configured %d/%d",
			ErrReplyRouteScope,
			persisted.OwnerUserID,
			persisted.PrivateChatID,
			ownerUserID,
			privateChatID,
		)
	}
	for index, record := range persisted.Routes {
		receipt := TelegramOutboundReceipt{MessageID: record.MessageID, SessionID: record.SessionID}
		if err := validateTelegramOutboundReceipt(receipt); err != nil {
			return nil, fmt.Errorf("validate Telegram reply route %d: %w", index, err)
		}
		if _, duplicate := routes[record.MessageID]; duplicate {
			return nil, fmt.Errorf("duplicate Telegram reply route for message %d", record.MessageID)
		}
		routes[record.MessageID] = record.SessionID
	}
	return routes, nil
}

func writeTelegramReplyRoutes(
	path string,
	ownerUserID int64,
	privateChatID int64,
	routes map[int64]domain.SessionID,
) (returnErr error) {
	records := make([]telegramReplyRouteRecord, 0, len(routes))
	for messageID, sessionID := range routes {
		records = append(records, telegramReplyRouteRecord{MessageID: messageID, SessionID: sessionID})
	}
	sort.Slice(records, func(left, right int) bool { return records[left].MessageID < records[right].MessageID })
	data, err := json.MarshalIndent(telegramReplyRouteFile{
		Version:       telegramReplyRouteFormatVersion,
		OwnerUserID:   ownerUserID,
		PrivateChatID: privateChatID,
		Routes:        records,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Telegram reply route store: %w", err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create Telegram reply route store candidate: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryOpen := true
	defer func() {
		if temporaryOpen {
			if closeErr := temporary.Close(); returnErr == nil && closeErr != nil {
				returnErr = fmt.Errorf("close Telegram reply route store candidate: %w", closeErr)
			}
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure Telegram reply route store candidate: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write Telegram reply route store candidate: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Telegram reply route store candidate: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Telegram reply route store candidate: %w", err)
	}
	temporaryOpen = false
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Telegram reply route store: %w", err)
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open Telegram reply route store directory: %w", err)
	}
	if err := directoryFile.Sync(); err != nil {
		_ = directoryFile.Close()
		return fmt.Errorf("sync Telegram reply route store directory: %w", err)
	}
	if err := directoryFile.Close(); err != nil {
		return fmt.Errorf("close Telegram reply route store directory: %w", err)
	}
	return nil
}

func validateTelegramOutboundReceipt(receipt TelegramOutboundReceipt) error {
	if receipt.MessageID <= 0 {
		return errors.New("positive Telegram outbound message id is required")
	}
	trimmed := strings.TrimSpace(string(receipt.SessionID))
	if trimmed == "" || string(receipt.SessionID) != trimmed {
		return errors.New("canonical session id is required for Telegram outbound receipt")
	}
	return nil
}

func cloneTelegramReplyRoutes(source map[int64]domain.SessionID) map[int64]domain.SessionID {
	clone := make(map[int64]domain.SessionID, len(source))
	for messageID, sessionID := range source {
		clone[messageID] = sessionID
	}
	return clone
}
